# TinyCode — CODEBASE Map

> AI coding agent in pure Go. Single binary, Bubble Tea TUI, ReAct agent loop, 25 built-in tools + MCP, LSP diagnostics, session persistence. 1270 test functions + 10 fuzz targets, race-detector clean.

## Quick Reference

| File / Dir | Purpose |
|------------|---------|
| `main.go` | Cobra CLI entry, wires all packages together |
| `agent/` | ReAct loop, LLM providers, context compression, agent registry, permissions |
| `config/` | JSON config loading (defaults → user global → project local → env/CLI) |
| `docs/` | Reference documents — `tui-verification.md` covers TUI verification |
| `internal/notify/` | Completion announcements (roadmap D5): OSC 9 + OSC 777 by default, the platform notifier (osascript/notify-send) when asked, one duration gate, and no sound or Windows path |
| `internal/netsafe/` | Shared SSRF policy: blocked-IP table, resolve-once + pinned-IP HTTP client, redirect re-validation, validated raw dialing (`DialValidatedContext`), optional loopback allowance |
| `internal/browserproxy/` | Loopback filtering HTTP proxy for Chromium: validates and pins every hostname the browser contacts (CONNECT tunnels and plain HTTP) |
| `goal/` | The persistent completion objective and the process-local permission to continue it (D7) |
| `lsp/` | LSP client (JSON-RPC over stdio), 4 tool wrappers, diagnostics |
| `mcp/` | Native MCP client (stdio + HTTP transports), JSON-RPC 2.0 |
| `session/` | Session persistence (JSON on disk), fork/branch, export, search |
| `skill/` | SKILL.md 3-layer discovery (builtin → global → project) |
| `tlog/` | Structured file logger with level filtering |
| `tool/` | 25 tool implementations (bash, edit, git, web, LSP, task, todo, sandbox, MCP, skill) |
| `tui/` | Bubble Tea TUI (CellGrid, viewport, markdown, selection, command palette) |
| `types/` | Shared types (Message, ToolCall, ChatRequest, StreamCallbacks, MemoryStore) |

---

## Package: `agent`

### `agent.go` — Core ReAct Loop

- **`Agent`** struct — fields:
  - `Config *AgentConfig` — mode config (plan/build/subagent)
  - `Provider LLMProvider`
  - `Resolver RouteResolver` — resolves a configured route to its provider; nil means the agent never routes. A route on `Config` is resolved at call time, so a session-wide switch does not override a pin (roadmap A5)
  - `Tools []Tool`
  - `Memory types.MemoryStore` — assigned by `main.go` when the memory section is enabled; the recall step below injects one system message per run from it (`memory_local.go`, local by default)
  - `MemoryMode int` — 0=none, 1=auto, 2=on-demand; `MemoryModeNone` is the only value ever in effect today
  - `SessionStore` — interface `{Append(msg types.Message) error; Flush() error}`
  - `History []types.Message` — multi-turn conversation; restored from the session on `--resume` (user and assistant turns only — command output and prior reasoning stay out) and appended by every ending through `recordTurn`
  - `CompressionThreshold int` (default 500000)
  - `ContextLength int` (default 1000000)
  - `discoveredCtxLen int` (unexported; lowered after `context_length_exceeded`)
  - `TodoStorer` — interface `{FormatForInjection() string}`
  - `SystemPrompt string`, `MaxSteps int`, `MaxTokens int`
  - `Verbose bool`, `ShowThinking bool`
  - `StreamCallbacks *types.StreamCallbacks`
  - `UsageTotal types.Usage` — every token the agent has spent: provider-reported records summed, plus an estimate of the output for a call whose endpoint reported nothing (so a silent provider cannot switch a budget off)
  - `BudgetTokensPerRun`, `BudgetTokensPerSession` (int) — cumulative token limits, 0 = unlimited; `runUsage` is the same accounting restricted to the run in flight, reset by each `Run`
  - `callSpend(resp)` → reported usage, or an output estimate marked as such; `recordCall(cb, resp, startedAt)` adds it to both totals and fires `OnUsage` only for the reported case; `startedAt` is the instant the request was sent, and it is what a time-of-day price is resolved against
  - `PriceTable`/`Price` (pricing.go) — the user's declared rates per billing lane; `Lookup` resolves `"<route>/<model>"` with `*` wildcards, most specific first (exact → `route/*` → `*/model` → `*/*`) and answers with the entry's own rates, while `AmountAt`/`Amount` carve the detail lanes out of the totals, falling back to the lane they belong to and flooring every subtraction at zero
  - `PriceWindow`, `NewWindow`, `(*PriceTable) SetEntry`, `AmountAt` (pricing_window.go) — a per-entry schedule: weekdays × UTC hours, `from` inclusive and `to` exclusive, `00:00`/`24:00` meaning the end of the day. `SetEntry` validates the whole set before storing any of it (no overlap on a shared day, the entry's own rates stay reachable, every lane the entry declares repeated in every window) and refuses a malformed one with a message naming the window; a refused entry prices nothing, so the call is unknown rather than priced by a guess. `Agent.Now` is the clock seam, read once per call before the request
  - `CostTotal types.CostTotals`, `CostUnknownCalls int` — money per unit, plus how many calls nobody could price; kept together because a total without its unknowns overstates what is known
  - `priceKey()` resolves the provider from the agent's route first and asks it (RouteInfo) for its route and default model at call time, preferring the agent's model override — a route can be switched mid-session, so a value captured at construction would price a call against another route's rates
  - `budgetReached()` / `budgetMessage(which, limit, spent)` — the check runs at the top of the loop, before the call that would spend, and its message names the budget, the spend and the knob (`budget.max_tokens_per_run` / `..._session`)
  - `ContentStreamed bool`
- **`New(provider LLMProvider) *Agent`** — defaults: MaxSteps=20, MaxTokens=4096
- **`(*Agent) AddTool(t Tool)`** — registers a tool
- **`(*Agent) Run(ctx context.Context, prompt string) (string, error)`** — core ReAct loop:
  1. Build messages: system prompt + compressed history + memory (only when `Memory != nil && MemoryMode == MemoryModeAuto`; the prompt is capped and the facts are worded as data — see `memory_recall.go`) + user prompt
  2. Loop up to `maxSteps`:
     - Filter tools by `ToolAllowedFor`
     - Call `Provider.Chat()` with streaming callbacks
     - No tool calls → `recordTurn` (history + session), return content
     - Empty response (no content, no tool calls) → append placeholder, retry
     - Has tool calls → append assistant message with all calls, execute concurrently via goroutines, collect results in index order. Each call goes through `toolcall.go` first: its arguments are checked against the tool's own schema (`ValidateArgs` — type/properties/required/enum/items, the five keywords the built-in schemas use, everything else ignored), and a call that breaks it is **not executed** but answered with every problem at once. A call whose exact identity (`toolCallKey`, key-order independent) has already failed `repeatFailureLimit` times in this run is refused as a loop instead of being tried again; successes are never counted, and the count resets with each run
     - Security block detection: result starting with `[SECURITY BLOCKED]` bypasses LLM
     - Fire `OnStepDone` callback after all tools complete
  3. Max steps reached: inject forced summary user message, one final LLM call with no tools, then record that summary with `recordTurn`
- **`(*Agent) recordTurn(user, assistant, reasoning string)`** — the one path that records a finished turn: appends the pair to `History` and, when a session store is attached, appends it there and flushes. Every ending goes through it — the final answer, the step-limit summary, a token budget and a terminal refusal — because the three early returns used to record nothing, so the next run (a human's "continue", or a goal round) could not read what the previous one did (issue #258). A turn with no assistant text is not recorded.
- **`(*Agent) CompressHistory(ctx context.Context) (bool, error)`** — compresses `a.History` in-place using the provider under the caller's context, so a cancel ends a stalled summarizer; reports whether the history shrank and the summarizer error, and leaves `a.History` untouched when it fails
- Constants: `MemoryModeNone=0`, `MemoryModeAuto=1`, `MemoryModeOnDemand=2`
- `securityBlockMarker = "[SECURITY BLOCKED]"`

### `config.go` — Agent Configuration

- **`AgentMode`** (string): `AgentModePrimary` ("primary") | `AgentModeSubagent` ("subagent")
- **`AgentConfig`** struct: `Name`, `Mode`, `Hidden bool`, `Description`, `SystemPrompt`, `MaxSteps int`, `Model string` ("<provider>/<model>"), `AllowedTools []string`, `DeniedTools []string`, `Permissions Ruleset`
- **`(*AgentConfig) IsToolAllowed(name string) bool`** — checks Permissions → DeniedTools → AllowedTools
- **`DefaultAgents() map[string]*AgentConfig`** — 6 built-in agents:

| Agent | Mode | Hidden | MaxSteps | Permissions |
|-------|------|--------|----------|-------------|
| `plan` | primary | | 20 | Whitelist: read_file, search_files, git_*, web_*, lsp_*, load_skill, todo |
| `build` | primary | | 50 | Allow all (`*`) |
| `explore` | subagent | | 15 | Only read_file, search_files |
| `general` | subagent | | 20 | All except task, task_collect, skill_manage, todo |
| `compact` | primary | ✓ | 1 | No tools (deny all) |
| `title` | primary | ✓ | 1 | No tools (deny all) |

### `registry.go` — Agent Registry

- **`Registry`** struct: `agents map[string]*AgentConfig`, `current string`, `order []string`, `pos int`
- **`NewRegistry() *Registry`** — registers all DefaultAgents, defaults to "plan"
- Methods: `Register(cfg)`, `Get(name) (*AgentConfig, error)`, `Current() *AgentConfig`, `CurrentName() string`, `Switch() string` (cycles primary non-hidden), `Set(name string) error`, `List() []*AgentConfig`, `ToolAllowed(toolName) bool`
- **`ToolAllowedFor(cfg *AgentConfig, toolName string) bool`** — package-level; checks Permissions (Ruleset) first, falls back to DeniedTools/AllowedTools

### `provider.go` — LLM Provider Abstraction

- **`LLMProvider`** interface: `Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)`, `Name() string`
- **`ProviderRecord`** struct: `Name string`, `Provider LLMProvider`
- **`ProviderRegistry`** — multi-provider manager:
  - `NewProviderRegistry(records []ProviderRecord) *ProviderRegistry`
  - `Current() LLMProvider`, `CurrentName() string`
  - `List() []ProviderRecord`, `Len() int`, `CurrentIndex() int`
  - `SwitchTo(idx int) error`, `SwitchToName(name string) error`
  - `CurrentRoute() string` — the active provider's configuration name, the identity a route reference and a saved session use (`Name()` is the backend/model display value)
  - `Resolve(route string) (LLMProvider, error)` — the `RouteResolver` implementation: empty means the active provider, an unknown name is an error rather than a fallback
- **`RouteResolver`** interface: `Resolve(route string) (LLMProvider, error)`; `Agent.Resolver` holds one (roadmap A5)
- **`ParseRoute(value) (route, model string, err error)`** (`route.go`) — cuts `"<route>/<model>"` on the **first** separator, so `openrouter/anthropic/claude-sonnet-4` is route `openrouter` + model `anthropic/claude-sonnet-4`; a value without a separator is a bare model override
- **`Tool`** struct: `Name`, `Description`, `Parameters map[string]any`, `Execute func(ctx, args) (string, error)`
- **`MockProvider`** — test double with injectable `ChatFunc`

### `provider_openai.go` — OpenAI-Compatible Provider

- **`retry.go`** — the policy for one provider call, shared by both providers:
  - `retryMaxAttempts` (3, the first attempt included), `retryBaseDelay` (500 ms) and `retryMaxDelay`
    (10 s) are package variables so a test shrinks the schedule instead of waiting for it;
    `retryJitter` is the injectable randomness source
  - `statusError{status, body, retryAfter}` with `Retryable()` (408, 429, any 5xx), `parseRetryAfter`
    (delta-seconds honoured, the HTTP-date form ignored on purpose), `backoffDelay` (exponential with
    equal jitter, floored at `Retry-After`, capped), `retryableError` (also refuses a context error and
    a protocol/parse error), `shouldRetry` (the whole decision: retryable **and** the budget has room
    **and** the context is alive **and** nothing was delivered), `sleepCtx`, `attemptsError` (names the
    count) and `notifyRetry`
  - `deliveryTracker` wraps a caller's callbacks to record whether any delta reached it — the
    **safe-replay** rule: text already drawn may not be drawn twice
- **`OpenAIProvider`** struct (unexported fields: client, model, apiKey, baseURL, route, costCurrency, cache, and the request seam `chatURL`/`authHeader`/`authValue`/`namePrefix` that the Azure route sets)
- **`NewOpenAIProvider(apiKey, baseURL, model string) *OpenAIProvider`**
- **`NewAzureOpenAIProvider(apiKey, baseURL, deployment, apiVersion string) *OpenAIProvider`** (`provider_openai.go`) — the same body, stream parser, retry, usage and prompt-cache path, addressed to `{base_url}/openai/deployments/{model}/chat/completions?api-version=…` with an `api-key` header. `model` is the deployment, so an agent-level model override selects a different deployment; `apiVersion` defaults to the pinned `2024-10-21` (`azureAPIVersion`). Only `prompt_cache: off` is accepted, because Azure caches automatically and has no request hint (roadmap A6)
- `SetPromptCache(PromptCachePolicy)` stores the route's cache policy (roadmap A4). `auto` adds a
  `prompt_cache_key` derived from the model, the leading system messages and the tool schemas — never
  from the conversation — so every step of every run sends the same routing hint (OpenAI's hint before
  GPT-5.6, OpenRouter's sticky-routing key). `explicit` also rewrites the *last leading system
  message* into one text block carrying `cache_control: {"type":"ephemeral"}` (plus `"ttl":"1h"` when
  configured); cache order is tools → system → messages, so that one breakpoint covers the tool
  schemas and the system prompt. `off`/absent sends neither field and leaves the body unchanged. The
  grammar (`off`/`auto`/`explicit`, `5m`/`1h`) and its validation live in `prompt_cache.go`
  (`ResolvePromptCache`); a value that cannot be honoured is a startup error naming the route.
- `Chat()` — SSE streaming via `net/http` (not go-openai SDK's streaming); accumulates reasoning/content deltas and tool calls by index; 64KB→256KB scanner buffer; `stream_options: {include_usage: true}`. The parse runs to `data: [DONE]` rather than stopping at `finish_reason`, because the usage chunk that answers that option comes *after* the finish chunk with an empty `choices` array; both it and the batch body's `usage` object land in `ChatResponse.Usage` (a pointer: "not reported" is not "zero"), and a `total_tokens` the endpoint omits is derived from the two halves
- Bounded (issue #176), with the same two-bounds shape as the Ollama provider: `openAIRequestTimeout` (120 s) covers one non-streaming request, while `openAIIdleTimeout` (2 min) is the silence allowed between lines of a stream and is reset by every line, so a long generation is not killed while a stalled one fails. Before that bound existed, reading past `finish_reason` (which is what makes the usage chunk reachable) meant an endpoint that went quiet after a complete answer stalled for the whole request timeout — and `chatStream` handed back the text it had with a nil error. A read that fails now returns `openai stream: …` instead, with `openAIRequestError` naming the bound that fired; a caller cancellation keeps `errors.Is(err, context.Canceled)` so a TUI run still reads as interrupted. Both bounds are package variables (tests shrink them), and **neither is armed on the `http.Client`**: a client timer also covers reading the body, where on a streaming call it is a total limit on the generation (issue #185) and, because its clock starts at the request while the idle watchdog rebases on every line, it fired first once a line had arrived and made the idle bound's own message unreachable at the shipped equal defaults (issue #186). The batch bound is therefore a watchdog on the request context, and the streaming path's only bound is the idle watchdog, which also covers the wait for response headers. A stream that *closes* without `[DONE]` is still accepted as complete: OpenAI-compatible servers vary here, and turning their working answers into errors would cost more than the truncation it would catch

### `provider_failover.go` — One Route, Then Its Fallbacks

- **`FailoverProvider`** — wraps a route with the routes its `providers[].failover` list names. `Chat` runs the primary (which already retries through `retry.go`), then each flattened fallback while the failure is one `retryableError` accepts and **nothing has been streamed** (one `deliveryTracker` spans the chain, so a delta from any attempt forbids the next). A fallback attempt clears `req.Model`: the fallback's own configured model is the one it knows.
- `NewFailoverProvider(primary, fallbacks)`, `Fallbacks()` (the flattened chain, for display and tests), `Name`/`Route`/`DefaultModel`/`CostCurrency` delegate to the primary — the wrapper is still the route a plan, a price key and a run record name.
- `failoverError` names every route that was tried and unwraps to the last failure; `failureKind` classifies an error (`rate_limit`, `server_error`, `timeout`, `network`, `protocol`, `status`) for the event and the log; `notifyFailover` calls `StreamCallbacks.OnFailover(from, to, err)` once per route change.
- Wired in `main.go`'s `wireFailover`, which flattens each declared list through the listed routes' own lists (a diamond dedupes, a cycle is refused), validates unknown names, self-references, a name listed twice, and wraps each route around its **raw** provider so a chain is followed once.

### `provider_ollama.go` — Ollama Provider

- **`OllamaProvider`** struct (unexported: baseURL, model, http client, route)
- `Chat()` retries through the same policy as the OpenAI-compatible provider (`retry.go`); each attempt
  builds its own cancel-cause context and idle watchdog, so a retry gets a fresh clock rather than the
  remainder of the one its predecessor spent
- **`NewOllamaProvider(baseURL, model string) *OllamaProvider`** — default baseURL: `http://localhost:11434`
- `Chat()` — line-delimited JSON (not SSE); tool results mapped to `role: "user"`; `thinking` field for reasoning; `prompt_eval_count`/`eval_count` are read into `ChatResponse.Usage` — from the stream's `{"done":true,…}` line (read before the loop breaks) and from the batch body's top level. Both are optional counters, so a server that reports neither leaves `Usage` nil instead of claiming a zero-token call. A local route reports no charge, so `CostCurrency()` is empty and its calls are priced from the declared table or counted as unknown. `ChatRequest.Model` wins over the route's configured model (it was ignored before A5, S2); every response is stamped with the route and model that served it
- `SetRouteInfo(route, model, _)` names the configuration route, so a price lookup keys on the route rather than on a model name several routes share; the cost currency is ignored because this route bills in none
- Bounded (issue #1): `ollamaRequestTimeout` (10 min) covers a whole non-streaming request, `ollamaIdleTimeout` (2 min) covers the silence between tokens on a stream and is reset by every line, so a long generation is not killed while a stalled one fails. Both are package variables (tests shrink them), and the cancel cause names the bound that fired instead of surfacing "context canceled". A stream that ends without its `{"done":true}` line is now an error rather than the partial text it managed to send.

### `provider_anthropic.go` — Native Anthropic Messages API

Roadmap A6. Implements `LLMProvider` against `POST {base_url}/v1/messages` with `x-api-key` and
`anthropic-version: 2023-06-01`; `base_url` defaults to `https://api.anthropic.com` and a value
already ending in `/v1` is accepted.

- **`AnthropicProvider`** (apiKey, baseURL, model, thinkingBudget, route/costCurrency, cache)
- `SetThinkingBudget(n)`, `SetPromptCache(policy)`, `SetRouteInfo(route, model, currency)`,
  `Route`/`DefaultModel`/`CostCurrency`, `Name() = "anthropic/<model>"`
- `anthropicMessages` maps the shared history: every system message becomes a top-level `system`
  text block (rather than a turn), an assistant turn becomes optional `thinking` + `text` +
  `tool_use` blocks, and a run of `tool` messages becomes **one** user message of `tool_result`
  blocks, which is the shape the API requires. A thinking block is replayed only when it carries the
  vendor's signature, because an unsigned or invented one is rejected
- Batch and SSE parsing (`message_start`, `content_block_start/delta/stop`, `message_delta`,
  `message_stop`, plus an `error` event): `text_delta` → content and `OnTextDelta`, `thinking_delta`
  → reasoning and `OnReasoningDelta`, `signature_delta` → `ChatResponse.ReasoningSignature`,
  `input_json_delta` → accumulated tool arguments per block index
- Usage: `input_tokens + cache_read_input_tokens + cache_creation_input_tokens` is `PromptTokens`
  (the API's `input_tokens` counts only the uncached half), `output_tokens` is `CompletionTokens`,
  and the two cache counters keep their own lanes
- `prompt_cache: explicit` marks the last leading system block with `cache_control`; a `1h` TTL adds
  `anthropic-beta: extended-cache-ttl-2025-04-11`. `auto` is refused at startup (no routing key)
- `thinking_budget_tokens` sends `{"type":"enabled","budget_tokens":N}`; the response's thinking text
  and signature travel back on `types.Message` so a tool step can replay them
- The same retry policy, bounds and route stamping as the other providers: `anthropicRequestTimeout`
  (120 s, batch), `anthropicIdleTimeout` (2 min, stream), no bound on the client

### `provider_gemini.go` — Native Gemini generateContent

Roadmap A6. Implements `POST {base_url}/models/{model}:generateContent` and
`:streamGenerateContent?alt=sse`, authenticated by `x-goog-api-key`; `base_url` defaults to
`https://generativelanguage.googleapis.com/v1beta` and a `models/` prefix on the configured model is
stripped.

- **`GeminiProvider`** (apiKey, baseURL, model, thinkingBudget, route/costCurrency, cache)
- `geminiContents` maps the history: system messages become `systemInstruction`, the assistant role
  is `model`, a tool call is a `functionCall` part whose `args` are the parsed JSON object, and a run
  of tool messages becomes one user content of `functionResponse` parts (`{"result": "<text>"}`)
- **`geminiSchema`** translates the shared JSON Schema into Gemini's `Schema` message: `type` values
  are upper-cased (`OBJECT`, `STRING`, …), `properties`/`items` recurse, and keys Gemini does not
  define are dropped rather than guessed
- Batch and SSE parsing: normal `text` parts → content, `thought: true` parts → reasoning and
  `OnReasoningDelta`, `functionCall` parts → `types.ToolCall` (`ID` is the function name, because
  Gemini has no call id and correlates a result by name)
- Usage: `promptTokenCount` → `PromptTokens`, `cachedContentTokenCount` → `CachedPromptTokens`,
  `candidatesTokenCount + thoughtsTokenCount` → `CompletionTokens` with `thoughtsTokenCount` as
  `ReasoningTokens` (candidates excludes thoughts, and the completion lane has to carry both)
- `thinking_budget_tokens` sends `thinkingConfig {includeThoughts: true, thinkingBudget: N}`;
  `prompt_cache: auto` is accepted as a no-op (Gemini caches automatically), `explicit` is refused
  until the `cachedContents` resource exists
- `geminiRequestTimeout` / `geminiIdleTimeout` and the shared retry/route stamping

### `provider_bedrock.go` — Native Bedrock Converse

Roadmap A6. Implements `POST /model/{modelId}/converse` and `/converse-stream` against
`https://bedrock-runtime.{region}.amazonaws.com`, signing every request with SigV4 (`bedrock`
service) from `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` and an optional `AWS_SESSION_TOKEN`.
Region: `providers[].region`, then `AWS_REGION`, then `AWS_DEFAULT_REGION`. No other credential
provider is implemented — reaching IMDS from an agent process is a separate security decision.

- **`BedrockProvider`** (accessKey, secretKey, sessionToken, region, baseURL, model,
  thinkingBudget, route/costCurrency, cache, and a `now` clock seam for the signing tests)
- `sign` builds the canonical request (host, `x-amz-date`, optional `x-amz-security-token`, sorted;
  SHA-256 payload) and the `AWS4-HMAC-SHA256` Authorization header
- `bedrockMessages` maps the history onto Converse: system blocks, `toolUse` blocks, and a run of
  tool messages into one user message of `toolResult` blocks; a signed reasoning block is replayed
  as `reasoningContent.reasoningText`
- **`readBedrockFrame`/`parseBedrockHeaders`** decode the binary `vnd.amazon.eventstream` framing:
  prelude length, header block, payload and **both CRC32s**, with a 16 MiB frame cap. Events:
  `messageStart`, `contentBlockStart`, `contentBlockDelta` (`text`, `reasoningContent`,
  `toolUse.input`), `contentBlockStop`, `messageStop`, `metadata`; an `exception`/`error` frame is
  returned as an error rather than a partial answer
- Usage: Converse's `inputTokens` is the whole prompt and `cacheReadInputTokens`/
  `cacheWriteInputTokens` are subsets, so they are copied into `PromptTokens`/the cache lanes;
  `totalTokens` is used when present and derived otherwise
- `prompt_cache: explicit` appends a `cachePoint` to the system array, or to the tool list when
  there is no system block; `auto` and a `1h` ttl are refused
- `thinking_budget_tokens` travels in `additionalModelRequestFields.thinking`
- `bedrockRequestTimeout` / `bedrockIdleTimeout` and the shared retry/route stamping

### `compression.go` — Context Compression

- **`EstimateTokens(text string) int`** — `len(text) / 4`
- **`EstimateMessagesTokens(msgs []types.Message) int`**
- **`ParseContextLimitFromError(errMsg string) int`** — regex extraction from API error
- **`(*Agent) compressHistory(ctx, messages) ([]types.Message, error)`** — algorithm (the head cut is snapped forward to a message-group boundary via `groupEnd`, so an assistant `tool_calls` message is never separated from its tool results):
  1. If tokens < threshold → no-op
  2. Need ≥4 user messages
  3. Head: first 2 user-message boundaries (+3 context after)
  4. Tail: last 2 user-message boundaries
  5. Middle → LLM summarization under the caller's context → `[COMPRESSED HISTORY]` system message;
     a **pinned** group is never fed to the summarizer and is re-inserted verbatim after the summary
     (`groupPinned`), and a middle that is entirely pinned costs no provider call (roadmap B5)
  6. Tool output in middle truncated to 200 chars during serialization
  7. Active TODO items re-injected after compression

  A summarizer failure (including `context.Canceled`) returns `nil, err` rather than a no-op history, so a caller can tell a cancelled summary from a healthy "nothing to compress". The agent loop ignores the returned history in that case, exactly as it did when the error was swallowed, but `CompressHistory` surfaces it to the TUI.
- **`(*Agent) HandleContextError(err error) bool`** — detects `context_length_exceeded`, lowers `discoveredCtxLen` (5 regex patterns, valid range 1024–10M, only lowers never raises)

### `context.go` — Turn-level Context Control

- **`(*Agent) PinLast(n)` / `UnpinLast(n)` / `DropLast(n)` / `PinnedTurns()`** — a *turn* is a user
  message plus everything up to the next one, the grouping `compressHistory` works in. Pins ride on
  `types.Message.Pinned` and are honoured by the compressor; `/drop` removes turns from the live history
  only — the session file is what `--resume` reads and is left alone. The TUI commands are `/pin [n]`,
  `/unpin [n]` and `/drop [n]`, refused while a run is active because they replace `Agent.History` while
  the run goroutine may append to it.

### `ruleset.go` — Permission Rules

- **`Effect`** (string): `EffectAllow = "allow"`, `EffectDeny = "deny"`
- **`Rule`** struct: `Action string` (tool name or `*`), `Resource string` (pattern or `*`), `Effect Effect`
- **`Ruleset`** = `[]Rule`
- **`Evaluate(action, resource string, rules ...Rule) Effect`** — last-match-wins, default Allow
- **`FilterTools(allTools []string, rules ...Rule) []string`** — filters out denied tools
- **`TranslateToolLists(base Ruleset, allowed, denied []string) Ruleset`** — converts config `allowed_tools` (whitelist) / `denied_tools` (additive) into rules, since the ruleset takes precedence
- `wildcardMatch(pattern, value string) bool` — `*` (any sequence) and `?` (single char) glob

### `memory.go` / `memory_local.go` — Long-term Memory

**Not a tool.** Recall is an agent-layer step (`MemoryModeAuto`, at the top of `Run`), never a
model-callable function, which is why `memory` is absent from the Complete Tool Name Registry above and
from every permission whitelist. `docs/memory-decision.md` is the policy behind it.

What runs today, in order:

1. `main.go` resolves the `memory` section with `UserLayer.ResolveMemory`, which honours a network
   backend or a `base_url` only from the user's own config file, and `memoryStore` refuses a
   network backend that names no `base_url` — there is no default endpoint to fall back to.
2. It builds the store with `memoryStore`: the local backend gets a per-project shard
   (`MemoryScopeFor`), the remote one gets the endpoint the user wrote — there is no default endpoint.
3. For the local backend it registers the directory with the tool layer (`tool.SetMemoryRoot`), so a
   tool that tries to write it is refused: a fact the agent can rewrite is not one.
4. Every run recalls at the top, with the prompt truncated to the recall cap
   (`memoryQuery`), and injects at most `memory.recall_limit` facts as one system message that says they
   are data, not instructions (`memoryMessage`). A store that cannot be reached is logged and skipped —
   it never fails the run.
5. The only writer is `/remember` in the TUI; `/memory` lists (id, label, value) and `/memory forget <id>`
   deletes. There is no summariser and no automatic write.
6. When `memory.embedding` is configured (roadmap B3), the local store is wrapped in `SemanticMemory`:
   `Remember` also embeds the fact, recall ranks by cosine over a sidecar of unit vectors (one file next
   to the facts, keyed by fact id and model), and the final order blends that with the lexical ranking.
   A fact written before embeddings were on is backfilled by a later recall, in one bounded batch; a
   model change drops the old vectors without touching a fact; any embedding failure falls back to the
   lexical ranking for that run.

- **`LocalMemory`** (`memory_local.go`) — one JSON object per line under `dir` (default
  `~/.tinycode/memory`), 0600, sharded per project, bounded by `max_entries` (2000) / `max_bytes`
  (8 MiB) with the oldest dropped first, deduplicated by an id derived from `(key, value)`, so
  remembering the same fact twice is one fact. Recall is lexical — ASCII words plus Han bigrams, tf-idf,
  a recency tiebreak — because embeddings are roadmap B3. `Forget` rewrites the file without the fact;
  a fact past the byte bound is refused rather than allowed to evict the store.
- **`HTTPMemoryClient`** (`memory.go`) — the Hindsight adapter, reachable only by explicit config.
  `Forget` reports `ErrMemoryDeleteUnsupported` rather than returning nil: the service has no
  per-memory delete, and the adapter's recall/list decoders predate its current API (issue #293, S4).
- Methods: `Remember(key, value string) error`, `Recall(query string, limit int) ([]types.Memory, error)`, `Forget(id string) error`, `List() ([]types.Memory, error)`
- Hindsight API: `base_url` is **required** (`memory.base_url`; there is no default endpoint and the client is not built without one), bank from `memory.bank` (default `default`), 15s timeout, auth via `HINDSIGHT_API_KEY` or `OPENAI_API_KEY`

### `truncate.go` — Output Truncation

- **`TruncationResult`** struct: `Content string` (preview + hint), `FullPath string`, `OmittedLines int`
- **`TruncateOutput(output string) TruncationResult`** — the preview keeps the **head**, the **tail** and a
  count of what fell between them: `... 18,431 of 20,000 lines omitted (4 mention an error, 9 a warning) ...`,
  where the counts are a deterministic scan (`truncErrorSignal` / `truncWarningSignal`), never a model call.
  A head-only slice lost the panic at the end, and a hint that said only "some bytes were dropped" could
  not tell a reader whether the dropped part mattered.
- Bounds: `TruncMaxLines` (2000), `TruncMaxBytes` (200KB), `TruncMaxTokens` (4000 — the bound that matters
  once the text is dense; `bytesPerToken` is `EstimateTokens`' inverse and a test pins the pair) and
  `TruncTailLines` (20). The token budget covers the whole preview: the tail is taken first, the head gets
  the rest. Overridable at startup with
  `SetTruncationConfig(maxLines, maxBytes, maxTokens, tailLines, outputDir)`; the saved full output is
  written 0600 because tool output can contain secrets.

---

## Package: `config`

### `config.go`

- **`Config`** struct (top-level):
  - `DefaultMode string`, `ShowThinking *bool`, `Verbose *bool`
  - `Providers []ProviderRecordConfig`
  - `Truncation *TruncationConfig`
  - `Budget *BudgetConfig`
  - `Goal *GoalConfig`
  - `Pricing *PricingConfig`
  - `Agents map[string]AgentOverride`
  - `Sandbox *SandboxConfig`
  - `Theme string`, `SessionDir string`
  - `LSP *LSPConfig`, `LogLevel string`
  - `ContextLength int` (default 1M), `CompressionThreshold int` (default 500K)
  - `SearXNGURL string`, `MCPServers []MCPServerConfig`
- **`ProviderRecordConfig`**: `Name`, `Type` (`"openai"` default, `"ollama"`, `"anthropic"`, `"gemini"`, `"bedrock"`, `"azure"`), `Model`, `BaseURL`, `APIKeyEnv`, `APIVersion` (Azure), `Region` (Bedrock), `ThinkingBudgetTokens` (native reasoning backends only), `PromptCache *PromptCacheConfig`, `Failover []string`, `CostCurrency` (the unit this route bills a *reported* charge in, e.g. `credits`; empty means the amount is displayed without a unit rather than labelled with a guessed one)
  - `PromptCacheConfig`: `Mode` (`"off"` default, `"auto"`, `"explicit"`) and `TTL` (`"5m"`, `"1h"`, explicit only). Resolved by `agent.ResolvePromptCache` when the provider is built, so an invalid value is a startup error rather than a silent no-op; which mode a backend accepts is decided by `validateBackendOptions` in `main.go` (compatible route: all three; Anthropic/Bedrock: `off`/`explicit`; Gemini: `off`/`auto`; Ollama/Azure: `off`)
  - `APIKey()` method: priority → `api_key_env` → `UPPER(NAME)_API_KEY` → the type's own variable (`ANTHROPIC_API_KEY`, `GEMINI_API_KEY` then `GOOGLE_API_KEY`, `AZURE_OPENAI_API_KEY`, `OPENAI_API_KEY`). `providerKeyFallbacks` holds the per-type list, so a native route never silently offers an OpenAI key
  - `ThinkingBudgetTokens` is refused at startup on a backend that has no such request field, and passed through unchanged to the three that do (Anthropic, Gemini, Bedrock)
- **`AgentOverride`**: `MaxSteps`, `AllowedTools`, `DeniedTools`, `SystemPrompt`, `Model`, `Permissions []AgentRule` (an explicit ruleset that replaces the agent's built-in one and wins over the tool lists)
- **`AgentRule`**: `Action`, `Resource`, `Effect` (`"allow"`/`"deny"`) — a config-local mirror of `agent.Rule`
- **`SandboxConfig`**: `ProjectRoot`, `DenyCommands []string`, `AllowedPaths []string`
- **`MCPServerConfig`**: `Name`, `Transport` ("stdio"|"http"), `Command`, `Args`, `Env`, `URL`, `Headers`
- **`LSPConfig`**: `Enabled bool`
- **`TruncationConfig`**: `MaxLines`, `MaxBytes`, `OutputDir`
- **`NotifyConfig`**: `Channel` (`terminal` | `desktop` | `both` | `off`) and `MinDurationSeconds` — the D5 policy; an absent section yields internal/notify's own default (terminal, 30 s) rather than silence, and a `0` means announce everything
- **`(cfg Config) NotifyPolicy() (channel string, minDuration time.Duration)`** — nil-safe accessor; `main.go` passes it to `notify.Configure`
- **`PricingConfig`**: `Currency` and `Prices map[string]PriceEntry` — what a route charges, keyed by `"<route>/<model>"` with `"*"` allowed in either position. The route is part of the key because a price belongs to a billing endpoint, not to a model name: the same model costs differently through a reseller, a gateway and the vendor's own API
- **`PriceEntry`**: `InputPerMillion`, `OutputPerMillion`, `CacheReadPerMillion`, `CacheWritePerMillion`, `ReasoningPerMillion`, `Windows []PriceWindowEntry` — a zero detail rate falls back to the lane it is carved out of (cached and written input to the input rate, reasoning to the output rate). These rates apply at every instant no window covers
- **`PriceWindowEntry`**: `Weekdays` (`mon`…`sun`, or full names, any case), `From`, `To` (HH:MM in UTC; `from` inclusive, `to` exclusive, `00:00`/`24:00` = midnight at the end of the day) and the same five lanes. It has to repeat every lane its entry declares — a lane left at zero would charge the entry's rate inside the window — and a window may not cross midnight (`TestAPriceWindowRepeatsTheEntrysLanes` holds the two spellings to the same keys)
- **`priceTable(cfg) (*agent.PriceTable, error)`** (in `main.go`) — reads `Pricing`, parses each window and builds the agent's table; an empty section prices nothing (nil, no error), which the agent reports as an unknown cost rather than as zero, while a malformed window stops startup with a message naming the entry and the window (issue #225). The five-lane mapping is written out field by field in `entryPrice`/`windowPrice` so a swap is visible, and both are priced lane by lane in `pricing_config_test.go`
- **`BudgetConfig`**: `MaxTokensPerRun`, `MaxTokensPerSession` — both default to 0 = unlimited; a negative value in a file is ignored rather than read as "no limit", so only zero switches a limit off
- **`(cfg Config) TokenBudgets() (perRun, perSession int)`** — nil-safe accessor used by `main.go` to set the agent's limits
- **`GoalConfig`**: `MaxGoalRounds`, `IdleGraceSeconds`, `NoProgressRounds` — the D7 continuation policy. A positive value replaces its default (256 / 2 / 3); zero, absent or negative takes the default, so a typo cannot remove a guard
- **`(cfg Config) GoalSettings() GoalPolicy`** — nil-safe accessor that fills in every unset field: `main.go` sets the goal service's default cap from it, and `NewTUI` reads the grace and the no-progress threshold
- **`DefaultConfig() Config`** — `deepseek-flash` (model version DeepSeek-V4.1-Flash, the name DeepSeek publishes; the `deepseek-v4-flash` alias is retired, issue #226), `https://api.deepseek.com`, 1M context
- **`loadFile(path) (Config, error)`** / **`decodeConfig(data) (Config, error)`** — `loadFile` reads one JSON file and hands the bytes to `decodeConfig`; the split exists so a layer that also needs the raw object decodes the same bytes instead of opening the file again (issue #231). `decodeConfig` drops every object key whose name begins with `$` (documentation, at any depth: `agents`, `pricing.prices`, an MCP server's `env`/`headers` all decode into maps, where a note would otherwise be read as an entry and cost the whole file, issue #229) and decodes the rest; values travel as `json.RawMessage` so their text is preserved. A malformed file is reported on stderr **once** and returned as an error, which the callers read as "no file"
- **`readConfigFile(path) ([]byte, error)`** — package-variable seam over `os.ReadFile`, so a test can count how often a layer is opened; production always uses `os.ReadFile`
- **`LoadLayers() Layers`** — one process start's read of both config files, one open each. `Layers.Config()` is the merged result (defaults → `~/.tinycode/config.json` → `./.tinycode/config.json`); `Layers.User.SandboxBoundary()` / `Layers.Project.SandboxBoundary()` are the widening keys per layer; `Layers.User.ConfineCommands()` and `Layers.User.AllowedPathGrants()` answer from the raw object. `main.go` builds this once and passes it down, so a malformed file costs one line rather than one per reader (issue #231)
- **`UserLayer`** — the user file read once: the decoded `Config`, the raw object (`confine_commands` is read there because `merge()` keeps only its "on" direction, issue #162) and the raw file's own read/decode error, which keeps `--list-grants` failing on a file it cannot parse
- **`ProjectLayer`** — the same for `./.tinycode/config.json`; a parse failure is reported by `loadFile` and leaves the layer empty
- **`LoadConfig() Config`** — merge order: defaults → `~/.tinycode/config.json` → `./.tinycode/config.json`; `merge` covers every section (providers, agents, sandbox, mcp_servers, theme, searxng_url, truncation, LSP, context limits); a malformed file is reported on stderr and the defaults are used. It reads both files for this call (`LoadLayers().Config()`)
- **`AddAllowedPath(path) error`** — patches only `sandbox.allowed_paths` in the *user* config (preserving unknown keys and never materialising defaults); used by "Always allow"
- **`LoadUserConfig() Config`** — defaults + user-global file only (no project layer); used when persisting settings so repo-controlled providers/system prompts are never promoted to the global config. It reads the file for this call, which is what the write-then-reload paths (`Save`, `AddAllowedPath`, `AddAllowedPathGrant`) need; the startup wiring passes the `UserLayer` it already read instead (issue #231)
- **`(cfg Config) Save() error`** — creates `~/.tinycode/` if needed and persists `config.json` with mode 0600

---

## Package: `tool`

### Tool Registration Pattern
Each tool exports a factory function returning `agent.Tool` with `Name`, `Description`, `Parameters` (JSON Schema), `Execute(ctx, args) (string, error)`.

### Complete Tool Name Registry

`main.go` registers the 25 tools below with `ag.AddTool`, and the `tool` package defines exactly these
(`Name: "…"` literals, plus the four `lsp` tool types). The last row is not one of them: MCP servers
discovered at startup are registered at runtime as `mcp_<server>_<tool>`, so `mcp_*` stands for a family
rather than for a tool. The table and the code are held together by
`TestDocumentedToolRegistryMatchesTheCode`, which reads both.

`memory` is **not** in this table and is not a tool — see the `memory.go` section above.

| Tool Name | Category | Source File |
|-----------|----------|-------------|
| `bash` | shell | `bash.go` |
| `read_file` | file | `filesystem.go` |
| `write_file` | file | `filesystem.go` |
| `search_files` | search | `filesystem.go` |
| `edit` | editing | `edit.go` |
| `apply_patch` | editing | `apply_patch.go` |
| `git_status` | git | `git.go` |
| `git_diff` | git | `git.go` |
| `git_commit` | git | `git.go` |
| `git_branch` | git | `git.go` |
| `git_log` | git | `git.go` |
| `web_search` | web | `web_search.go` |
| `web_extract` | web | `web_extract.go` |
| `web_extract_browser` | web | `web_browser.go` |
| `task` | sub-agent | `task.go` |
| `task_collect` | sub-agent | `task.go` |
| `todo` | task mgmt | `todo.go` |
| `goal` | objective | `goal.go` |
| `sandbox_allow` | security | `sandbox.go` |
| `load_skill` | skill | `load_skill.go` |
| `skill_manage` | skill | `skill_manage.go` |
| `lsp_definition` | LSP | `lsp/tools.go` |
| `lsp_references` | LSP | `lsp/tools.go` |
| `lsp_hover` | LSP | `lsp/tools.go` |
| `lsp_symbols` | LSP | `lsp/tools.go` |
| `mcp_*` | MCP (dynamic) | `mcp.go` |

### Key Tool Implementations

- **`bash`**: Shell execution with plan-mode write blocking (mkdir, rm, mv, cp, heredoc, file redirect; reads the restriction from the run context) + sandbox command blocklist; runs in its own process group, and on timeout the kill walks the process tree first so a `setsid(2)` descendant cannot escape (`tool/sysproc_unix.go` + `tool/procchildren_*.go`, `WaitDelay` guards the pipes); caps each stream at 1 MiB
- **`read_file`**: 2000-line limit, offset/limit paging, LSP warmup fire-and-forget, sandbox path check
- **`write_file`**: Creates parent dirs, LSP baseline + diagnostics
- **`search_files`**: path-sandbox gated (searching reads file contents); priority `rg` → `grep` → Go native `filepath.Walk`, and a rung that is installed but *fails* (a broken shim, a rejected flag) falls through to the next one instead of failing the search — the portable walk is always available (issue #4). The native walk skips dotfiles, dot-directories and binary files (a NUL byte in the first 512 bytes), and an unreadable directory costs its own files and nothing else
- **`edit`**: 7 fuzzy strategies (exact → line-trimmed → ws-normalized → indent-flexible → escape-normalized → unicode-normalized → block-anchor with Levenshtein ≥ 0.65) + indentation correction
- **`apply_patch`**: V4A format (`*** Begin Patch / *** Update File: / *** Add File: / *** Delete File: / *** End Patch`), 3 phases: parse → validate → apply; every target path passes the shared sandbox gate before any I/O
- **`web_search`**: DuckDuckGo Lite (zero config) + optional SearXNG fallback (`SetSearXNG(baseURL)`)
- **`web_extract`**: 5-level fallback (HTTP → Cloudflare → Google Cache → Wayback → Chromium), SSRF protection via `internal/netsafe`, LLM summarization for >5000 chars (`SetSummarizer(fn)`)
- **Browser detection** (`tool/web_browser.go`): `browserCandidates` orders them — `CHROME_PATH`/`CHROME` (what CI's `setup-chrome` exposes), then the system commands, then the Playwright cache — and `firstUsableBrowser` probes each with `--version` before returning it, memoized per path. The probe is what survives a broken installation: the runner's `/usr/bin/chromium-browser` shim never answers, so it is skipped instead of being handed to an extractor that then hangs for 35 s; without a usable candidate `findBrowser` returns "" and the rod path falls back to its own launcher. `findPlaywrightBrowser` accepts every layout Playwright has shipped (the current `Google Chrome for Testing.app` under `chrome-mac-arm64`/`chrome-mac-x64`, the older `chrome-mac/Chromium.app`, the linux/windows equivalents and `chrome_headless_shell`), newest revision first and the full browser before the shell. `tryBrowser` (the `--dump-dom` fallback in `web_extract.go`) and the browser tool's automated path use `findExecBrowser` instead of a second LookPath loop: same chain, but the Playwright *headless shell* is preferred over the full browser, because a desktop build can refuse to dump at all (measured on macOS: no DOM in 120 s from "Google Chrome for Testing", about a second from `chrome-headless-shell` of the same revision — issue #43). The rod path keeps `findBrowser` (full browser first). An explicit `CHROME_PATH`/`CHROME` or a working system command still wins over both, so CI's `setup-chrome` browser is unaffected. The single rod launcher passes `browserContainerFlags` (`--no-sandbox`), because a CI container denies Chromium a user namespace and it otherwise aborts in the zygote with "No usable sandbox!" before publishing a debug URL; there is no flags-less retry any more — a launch without the sandbox flags is refused instead of silently downgrading (issue #8, see the known-limit entry for the filtering proxy). `execBrowserArgs` (pure, unit-tested) builds the `--dump-dom` argument list: no `--single-process` (Chromium documents it as unsupported and it aborts while rendering on the runner), a throwaway `--user-data-dir` from `os.MkdirTemp` (extraction never touches the user's real profile, and it works where that profile is not writable), `--disable-breakpad`, and on Linux `--disable-dev-shm-usage`. When Chromium fails, the error carries the last three stderr lines (`lastLines`), because a core dump in CI leaves no other trace.
- **`task`**: Sub-agent delegation (explore/general), sync or background mode, 120s timeout; a timed-out or cancelled sync task cancels the sub-agent's context (the result channel is buffered so the goroutine always exits)
- **`todo`**: CRUD with `TodoStore` (max 256 items, 4000 chars/item, one in_progress); every method takes an `RWMutex` and `Read` returns a copy. The store is one per process but its **contents** are session state: `persistSession` writes `Session.Todos` and `adoptSession` replaces the list on `--resume`, `/session` and `/fork` (issue #270)
- **`goal`**: the model-facing half of D7. `action` is one of `get`/`create`/`edit`/`pause`/`resume`/`complete`/`blocked`; every mutation echoes the `goal_id` and `revision` a `get` returned, and a stale revision is refused. Authority is the point: `create`/`edit`/`pause`/`resume` need a turn a human started (`types.InGoalRound` is false), `complete`/`blocked` also work inside a goal round, and a sub-agent may only read (`types.InSubagent`). It returns `{goal, armed}` JSON, with `goal: null` when none is current; `blocked` records the code `model-reported` and the reason the model gave
- **`sandbox_allow`**: Interactive permission dialog (Allow once / Allow session / Always allow / Deny)

### `sandbox.go` — Security Sandbox

- **`SandboxConfig`**: `ProjectRoot`, `DenyCommands []string`, `AutoAllowPaths []string`, `allowedPaths map[string]bool` (mutex-guarded)
- **`DefaultSandbox`** — global with deny: `rm -rf /`, `sudo`, `dd`, `mkfs`, fork bomb, etc.
- **`AccessDenied`** error: `Path`, `Message`, `DenyHint() string`
- **`CheckCommand(cmd string) error`** — blocklist matching (substring match; not a security boundary)
- **`CheckPath(absPath string) error`** — kernel-order symlink resolution (`resolveRealPath`, applied to the raw path before any lexical clean, so `link/..` cannot escape) + project-root containment + auto-allow + cached allows
- **`SetInteractive(bool)` / `IsInteractive()`** — declare whether a permission dialog can be shown. Default true; a one-shot CLI run calls `SetInteractive(false)`, and a denial then returns `AccessDenied.NonInteractiveHint()` immediately instead of blocking in `RequestPermission` forever
- **`CheckPathAccess(ctx, path) (string, error)`** — the shared gate used by `read_file`, `write_file`, `edit`, `apply_patch`, `search_files`; returns a user-facing denial message or ("", nil)
- **`WithPathGate(t agent.Tool) agent.Tool`** — wraps a tool implemented in another package (the `lsp_*` tools) so its `path`/`file_path` argument goes through the same gate
- **`RequestPermission(ctx, path) (bool, string)`** — enqueues a FIFO request and blocks on its own channel until the TUI resolves it or ctx is cancelled
- **`SetAgentLabel(label string)`** — sub-agent label for dialog display
- **`ResolvePermissionByID(id, allow, mode) bool`** — the safe dialog API: answers the request the user was shown even if the queue head changed; **`ResolvePermission(path, allow, mode) bool`** — path-based variant (empty path = head) used by `sandbox_allow` and tests; `once` is never cached, `session`/`always` populate `allowedPaths`
- **`HasPendingPermission()`, `PendingPermissionPath()`, `CancelPendingPermission()`** — display/teardown helpers

### `pathbeneath*.go` — Kernel Containment Layer

- **`relBeneath(root, path) (string, bool)`** (portable) — the path remainder the Linux layer hands to `openat2`, computed *without* cleaning `..` so the kernel resolves exactly what the real open would; only separators and `.` are dropped
- **`kernelEscapeCheck(root, path) bool`** (Linux) — `openat2(RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS)` probe, inert on kernels < 5.6 (`openat2Unsupported`) and a no-op elsewhere (`pathbeneath_other.go`)
- **`openResolvedNoFollow(path, flags, perm)`** (Linux + macOS, `pathbeneath_walk.go`) — walks an already-resolved absolute path component by component with `O_NOFOLLOW`, so a component that is a symlink at open time is refused with `ELOOP`; `openDirFlags` is `O_PATH` on Linux (`pathbeneath_linux.go`, works without read permission on the directory) and `O_RDONLY` on macOS (`pathbeneath_darwin.go`, where `O_PATH` does not exist — the same choice the standard library's `os.Root` makes). Unsupported platforms return `errBeneathUnsupported` (`pathbeneath_walk_other.go`)
- `RESOLVE_BENEATH` rejects absolute symlinks wherever they point, so `CheckPath` probes the OS-resolved form (`realAbs`), never the raw path; probing the raw path denied in-root symlinks

### `confinement_*.go` — the command boundary

The fence above confines the agent's own opens. A command the agent spawns is a
different question with a different mechanism: the process that applies the
boundary has to be a process of its own, so `main` intercepts `__sandbox-exec`
before any flag parsing and re-execs itself as the launcher.

- **`runConfined(launcherSpec) int`** — never returns on success: it replaces the process with the confined command. Its only return value is a failure status, and every failure path also writes the `tinycode-sandbox:` diagnostic, because "the boundary was not applied" and "the command exited with a status" must not be confusable.
- **Linux** (`confinement_linux.go`) — Landlock: a ruleset that handles the write-class rights the running kernel's ABI understands (`REFER` from ABI 2, `TRUNCATE` from ABI 3, because handling a right the kernel does not know makes ruleset creation fail outright), `/dev/null` granted in every mode, `workspace-write` granting the caller's roots, then `PR_SET_NO_NEW_PRIVS` and `landlock_restrict_self` before the exec.
- **macOS** (`confinement_darwin.go`) — Seatbelt through `/usr/bin/sandbox-exec`: `(allow default)`, `(deny file-write*)`, `(literal "/dev/null")`, then one `(subpath …)` per root. Roots are resolved with `filepath.EvalSymlinks` first, because Seatbelt matches the resolved path — a rule for `/tmp/x` matches nothing while the command can still address the file as `/tmp/x`. The launcher resolves the command itself before the exec (sandbox-exec reports a missing one as its own status 71, not as this contract's 125) and passes the argv unchanged, since `sandbox-exec` execvp's `argv[0]` and the command's own `argv[0]` therefore survives as it does on Linux. The capability answer is a **smoke run**, not a stat: it decides the default of `confine_commands` and a false positive would turn confinement on and then refuse every command.
- **Neither** (`confinement_other.go`) — `launcherFail("no kernel file-boundary mechanism for subprocesses on this platform")`.

### `checkpoint.go` — Pre-images, undo and retention

- **`recordPreImage(ctx, path)`** — called by `writeSandboxed` (and `removeSandboxed`) *before* the replacement, so every content-changing call site records what it is about to overwrite; a context with no `types.ToolCallScope` records nothing, which is what keeps callers outside the agent loop unchanged. **Fail closed**: a pre-image that cannot be read for any reason other than "does not exist yet" refuses the write instead of performing it unundoably
- **`CheckpointCall` / `CheckpointEntry`** — one manifest per tool call (tool, session, run/step/call, time, `restored_at`) holding one entry per write (path, `existed`, SHA-256, mode). Pre-images are content-addressed under `objects/<aa>/<sha256>`, so identical bytes are stored once; the store is `checkpoint_dir` (default `~/.tinycode/checkpoints`) keyed per project, deliberately outside `sandbox.project_root` so the fence confines it too, and `CheckPathAccess` refuses any tool path beneath it with `ask: reserved`
- **`LastCall(session)` / `RestoreCall(ctx, call)`** — the newest call that has not been undone, and its restore. A restore marks the call undone in its own manifest rather than deleting it, which is what makes the next undo reach the call before it; a restore that fails partway returns the paths already restored and leaves the call unmarked, so a retry is safe. A call attributed to another session is skipped, an unattributed one stays a candidate (the TUI mints its session id after the first call it records)
- **`pruneCheckpoints(store)`** — the bound: 50 calls or 64 MiB per project (`checkpointMaxCalls` / `checkpointMaxBytes`), dropping the oldest **already-undone** calls first and then collecting objects no remaining manifest references. A call that can still be undone is never dropped, so a project with more live calls than the limit stays over it on purpose. `/undo`, `--undo` and the `/sandbox` report are the surfaces; `docs/checkpoints.md` is the user-facing page, including the boundary that a `bash` command's own writes are not recorded

### `trajectory*.go` — What a run leaves behind

- **`Trajectory` / `TrajectoryRun`** (`agent/trajectory.go`) — one JSONL file per run, one event per line, each event carrying its own duration so a reader never pairs two lines to compute one. `run.start` (which carries `goal{goal_id, round, source}` when the run serves a goal), `llm.call` (which carries `route`/`model` — the route that served the call — plus `route.failover` events when a call moved, roadmap A5), `tool.call`, `step.end`, `run.end`; payloads are `{sha256, chars}` and carry `text` only under `capture: "full"` (bounded at 64 KiB). Files are created `O_EXCL` under a timestamp-and-counter name, so two processes starting in the same second cannot overwrite each other's record. `Prune` keeps the newest `max_runs` / `max_bytes`, never removing a file some run still holds open.
- **`agent/trajectory_agent.go`** — where the loop meets it. `recording()` is `Trajectory != nil || TrajectoryParent != nil` (a delegated agent has no writer of its own). `Run` records from its first instant, and one `defer` writes `run.end`, so the six ways a run can return — including a panic — all close the file with the same vocabulary: `final`, `budget`, `blocked`, `step_limit`, `cancelled`, `error`. Durations come from `Agent.Now`; with nothing being recorded the clock is still read exactly once per call, which is the pricing invariant #225 established — the second read is the recorder's and only happens when a trajectory exists. `recordFailover` attaches a route change to the step that made it and forwards it to the caller; `toolStatus` classifies a tool result from its text (`ok`, `error`, `blocked`, `refused`, `repeated`), because the dispatcher turns every failure into a string and there is no separate status to read.
- **`agent/trajectory_read.go`** — the reader `--trajectories` uses. It parses the files rather than an index, reports a file with no `run.end` as incomplete rather than broken, tolerates a half-written last line, and `ValidRunID` is the path-traversal guard: the id becomes a file name, and the store is worth nothing if it can be asked for a file it never wrote.
- **Not a tool path** (`tool/trajectory_store.go`): `CheckPathAccess` refuses the store with `ask: reserved`, full access excepted, for the reason the checkpoint store is refused — evidence the recorded agent can rewrite is not evidence.
- **Never fails a run**: a write failure warns once and disables the recording. The checkpoint store does the opposite on purpose (a pre-image that cannot be read refuses the write); one is safety, the other is observability, and `docs/checkpoints.md` and `docs/trajectories.md` each say which they are.

### `eval/` — The evaluation spike

Not product code, and deliberately small: it answers whether an evaluation signal exists at all (issue
#252), so that roadmap F1 is argued about with a number rather than an intention.

- **`eval/tasks/`** — eighteen self-contained tasks: a `go.mod`, a real defect, a test that fails, a one-line
  `PROMPT` and a declared `TIER` (1 one file and an obvious defect, 2 a contract between files, 3 a
  symptom-only prompt whose tests are the specification). No dependencies, so a task runs offline. The third
  increment added six chosen to be hard for a stated reason — an algorithmic bound the suite times, an
  implementation from a specification, five defects at once, one wire format produced by three sites,
  all-or-nothing state, an exhaustive format with a round-trip.
- **`eval/_solutions/`** — a reference fix per task, so "the task is solvable" is a gate rather than a claim:
  `go test ./eval/runner` proves each task is red before its fix and green after, and that the fix never
  touches a test. The directory leads with an underscore so the go tool ignores it — the fix is a file to
  copy, not a package to build. It is outside `eval/tasks/` for a sharper reason: `copyTask` copies a task
  directory into the work copy, and a solution inside it would be handed to the agent.
- **`eval/runner/`** — `TINYCODE_EVAL=1 go run ./eval/runner`. For each (configuration × task × repetition)
  it copies the task to a scratch directory, writes that copy's own `.tinycode/config.json` (the agent mode
  under test, the hard token budget, and the attempt's record directory), runs the binary, and grades the
  attempt by the task's own `go test ./...`. Test files are hashed before and after: an attempt that edited a
  test is recorded as tampered and counts as a failure.
- **`aggregate.go`** — everything that decides a verdict, kept pure so it is tested without spending money —
  including `judgeHeadroom` (can any task defeat the default configuration, while the set still contains one it
  solves) and `measureDifficulty` (the tasks ordered by the tokens that configuration actually spent, banded
  into thirds, because the previous increment showed a declared tier is a guess):
  solved tasks by majority of repetitions, the noise reading (did the repetitions agree), tokens per solved
  task including the failures' spend, per-tier rows, `judge` (the three-clause separation condition) and
  `judgeGradient` (build must solve a strictly smaller *fraction* of tier 3 than of tier 1 — fractions
  because the tiers do not hold the same number of tasks).
- **Why it reads trajectories** — steps, tokens, cost, tools and `stop` come from `agent.ListRuns`, not from
  the runner's own bookkeeping. That makes the harness the record's second consumer, which is how a format
  gets found out when it is missing a field.

### `snapshot.go` — A command's pre-images

- **`beginCommandSnapshot(ctx)`** — called by the bash tool before it starts a command, because a shell command never says what it will touch. It walks the project root, skipping `.git`, the checkpoint store and non-regular files, reads and hashes every file under the per-file cap (2 MiB) into the content-addressed store, and remembers size/mtime/inode as the change detector. **It never fails the command**: over the whole-snapshot bound (32 MiB / 4000 files), on an unreadable tree, or without a store, it sets the reason and the tool result carries it — the opposite of the file tools, whose whole purpose is the write they refuse without a pre-image. The bounds are the measurement in `docs/checkpoints.md` (42.9 ms / 379 files here), not a guess.
- **`(*treeSnapshot).finish(ctx)`** — deferred in the bash tool, so it runs on success, failure, timeout and cancellation: a killed command's changes are the ones an undo is most likely wanted for. It walks the tree again (a stat per path, no second read), turns moved/created/removed paths into manifest entries, appends them in one manifest write, and runs the same prune a file-tool write does. A command that changed nothing writes no manifest, so the store only ever holds what an undo may need.
- **`uncaptured`** — the paths that existed but were not read (over the per-file cap, or unreadable). They are tracked separately from "not in the snapshot", because the two mean opposite things: an uncaptured path must be left alone (recording it as new would make an undo *delete* a file the command merely edited), while a path the snapshot never saw is one the command created.
- **`inodeOf`** (`inode_unix.go`, `*_other.go`) — the inode is what catches a rename over the path when size and mtime coincide; the portable build returns 0 and the diff falls back to size and mtime.

### `bgtask.go` — Background Task Manager

- **`TaskState`** (int): `TaskRunning`, `TaskDone`, `TaskFailed`, `TaskTimedOut`
- **`BgTask`**: `ID`, `Agent`, `Goal`, `State`, `Result`, `Error`, `Done chan`
- **`BackgroundTaskManager`**: `NewBackgroundTaskManager()`, `Start(deps, name, goal) string`, `Collect(taskID) (string, error)`, `CollectContext(ctx, taskID)`, `Status(taskID) string`; all state writes go through a locked `finalize` — idempotent, the whole transition under one `sync.Once`, so the worker and the watchdog cannot settle a task twice — and a watchdog settles `TaskTimedOut` at the deadline so `Collect` cannot hang. The completion is announced by `internal/notify` from *outside* the lock (a terminal write, and on the desktop channel a spawned process, are not work to hold the manager for) and exactly once, because the once-guard covers the announcement too

---

## Package: `lsp`

### `client.go`
- **`Client`** struct: wraps `*Conn`
- **`NewClient(conn *Conn) *Client`**
- Methods: `Initialize(rootURI) error`, `Shutdown() error`, `GoToDefinition(uri, line, char) (*Location, error)`, `FindReferences(uri, line, char) ([]Location, error)`, `Hover(uri, line, char) (*Hover, error)`, `DocumentSymbols(uri) ([]SymbolInformation, error)`, `Diagnostics(uri, content) ([]Diagnostic, error)` (5s timeout)
- **`Diagnostic`** struct: `Range`, `Severity`, `Message`
- **`Initialize(rootURI)`** — sends both `rootUri` and `workspaceFolders` (gopls 0.23 ignores `rootUri` alone)
- **`SyncDocument(uri, content)`** / **`languageIDForPath`** — opens a document with its real text and switches to `didChange` on later syncs; the language id comes from the file extension. Sending an empty buffer (or a duplicate `didOpen`, which servers ignore) used to make gopls report phantom errors and miss real ones.
- **`Initialize`** declares `textDocument.publishDiagnostics` support, which is not decoration: `typescript-language-server` gates every diagnostic on that capability and publishes nothing without it, so every TypeScript file looked clean; gopls pushes regardless, which is why the empty capability map went unnoticed until a second server was exercised (issue #10). Pinned ungated by `TestInitializeAdvertisesDiagnosticsSupport`.
- **`touch.go`**: `Init(root)` shuts a running server down when the workspace changes (a server is bound to the root it started with), `SyncFile(path, content, withDiagnostics)` opens/updates the document from bytes the *caller* already holds — the read it did through the sandbox, or the content it just wrote — and uses the OS-resolved (`canonicalPath`) path for the URI; the server process runs with `Dir` = the project. The package opens no file itself (issue #7 S2): a plain `os.ReadFile` here would re-open a path whose sandbox decision was made for an earlier read, so a component swapped for a symlink in between would be followed and its content handed to the server. `SnapshotBaseline(path, content)` and `GetNewDiagnostics(path, content)` take the pre-edit and post-write bytes for the same reason.
- **`serverLanguage(projectRoot, filePath)`** / **`languageForPath`** — picks the server: project detection (`DetectLanguage`) first, then the touched file's own extension. A file no server handles returns `""`, which `lazyStart` turns into an error — it is never silently handed to `gopls`, whose "not included in your workspace" answers used to look like real diagnostics.
- **`diagnostics.go`**: in-memory registry of the latest severity-1 diagnostics per file, updated whenever a `publishDiagnostics` push arrives; `DiagnosticsSnapshot() DiagnosticsInfo`, `DiagnosticsSummary() (files, errors int)`, `DiagnosticsDetails() []string`, reset by `Init(root)`

### `conn.go`
- **`Conn`** — JSON-RPC over stdio pipes, Content-Length framing
- Methods: `Send(method, params) (json.RawMessage, error)` (unique id per request, 30s `requestTimeout`), `Notify(method, params) error`, `Close() error` (idempotent, unblocks waiters), `StartReader()` (`sync.Once`; the **only** goroutine that reads the stream, dispatching responses to per-id channels and `publishDiagnostics` to `diagChan`, and answering server-initiated requests such as `workspace/configuration` instead of dropping them)

### `server.go`
- **`Server`** struct: `{cmd *exec.Cmd, Client *Client, Conn *Conn}`
- **`Config`** struct: `{Language, Command, Args []string}`
- `Start(ctx, command, args...) (*Server, error)`, `(s *Server) Close() error`
- `FindConfig(language string) *Config`
- `DetectLanguage(rootDir string) string`
- `DefaultConfigs`: gopls, pyright, typescript-language-server, rust-analyzer, clangd, eclipse.jdt.ls

### `tools.go`
- **`ToolType`** (string) constants: `ToolGoToDefinition`, `ToolFindReferences`, `ToolHover`, `ToolDocumentSymbols`
- **`ToolFactory(tt ToolType) agent.Tool`** — creates LSP tool; prefers persistent connection, falls back to per-call spawn

### `formatter.go`
- **`FormatDiagnostics(filePath string, diags []Diagnostic) string`** — ERROR severity only, max 20 per file

### `types.go`
- LSP protocol types: `Position`, `Range`, `Location`, `SymbolInformation`, `Hover`, `MarkupContent`, `JSONRPCMessage`, `JSONRPCError`, etc.

---

## Package: `mcp`

### `mcp.go`
- **`MCPClient`** interface (all calls ctx-aware): `Initialize(ctx)`, `ListTools(ctx)`, `CallTool(ctx, name, args)`, `ListResources(ctx)`, `ReadResource(ctx, uri)`, `Close() error`, `Tools() []Tool`, `Info() *ServerInfo`
- **`Client`** — stdio-based MCP client implementing MCPClient
  - `NewClient(stdin, stdout, stderr) *Client`
  - Protocol: JSON-RPC 2.0, Content-Length framing, version `2025-03-26`
  - One request/response exchange at a time (`mu` held across write+read); responses are matched by id, id-less notifications and mismatches are skipped up to `maxSkippedMessages`
  - Bounded (issue #2): `defaultRequestTimeout` (5 min) is applied in `send` when the caller's context has no deadline of its own — the handshake's 60 s bound covers `initialize`/`tools/list` only, and every later `tools/call` inherits the agent's deadline-free run context. A caller-supplied deadline is never replaced, `0` disables the bound, and the failure names it (`no response within 5m0s`) instead of a bare `context canceled`
  - A response dispatched in the same instant the transport died is still delivered (issue #39); `destroy`/`chargeUnmatched` closing the channel means no response will come
  - Framing: `strconv.Atoi` on a case-insensitive `Content-Length`, negative/oversized (>8 MiB) rejected, 8 KiB header-line cap
  - `Close()` is idempotent and unblocks a blocked exchange; `SetKillFunc` lets the transport owner reap the process
- Types: `Tool`, `ToolResult`, `ToolResultContent`, `Resource`, `ResourceResult`, `ResourceContent`, `ServerInfo`

### `transport_http.go`
- **`HTTPClient`** — HTTP POST transport implementing MCPClient
- **`NewHTTPClient(baseURL, headers) *HTTPClient`**
- POSTs JSON-RPC to a single endpoint with `netsafe.NewClient(httpRequestTimeout, true)` (loopback allowed only when the configured endpoint itself is loopback) + `NewRequestWithContext`; rejects responses whose id does not match; reads up to 2MB responses
- SSRF protection: blocks private IPs, cloud metadata; localhost allowed for dev

### `tool/mcp.go` (bridge)
- **`ConnectMCPServers(ctx, servers []config.MCPServerConfig) ([]agent.Tool, error)`** — stdio children run under a process-lifetime context (the handshake timeout applies to `initialize`/`tools/list` only), the child is killed and `Wait`ed on handshake failure, and `kill`+reap is registered with the client
- Tools registered as `mcp_<serverName>_<toolName>` + `mcp_<serverName>_list_resources`
- Supports stdio + HTTP transports

---

## Package: `session`

### `session.go`

- **`Session`** struct: `ID`, `Title`, `Preview`, `ModelName`, `CreatedAt`, `UpdatedAt`, `MessageCount`, `ParentSessionID`, `ForkAt int`, `AllowedPaths []string`, `Messages []types.Message`, `Todos []types.TodoItem`, `Goal *goal.Goal`, `dir string` — the task list and the goal are session state, stored beside the transcript; activation is not, so a resumed session is disarmed — the goal is stored here because the session file is the only durable log a resume reads; activation is not, so a resumed session is disarmed
- **`New(id, dir string) *Session`**
- Methods: `Append(msg types.Message) error`, `Flush() error` (atomic temp-file+rename write, mode 0600, derives title/preview), `ExportMarkdown() string` (takes the session lock while reading messages)
- **`Store`** struct:
  - `NewStore(dir string) *Store`
  - `Create(id string) *Session`
  - `Load(id string) (*Session, error)`
  - `Delete(id string) error`
  - `Fork(parentID string, forkAt int, label string) (*Session, error)` — branch session; `forkAt` is clamped to the persisted message count and an already-used explicit label is rejected
  - `Search(query string) []SessionInfo`
  - `List() []SessionInfo`
- **`SessionInfo`** struct: `ID`, `Title`, `Preview`, `ModelName`, `CreatedAt`, `UpdatedAt`, `MessageCount`
- **`ExportMarkdown() string`** — session to markdown

---

## Package: `goal`

### `goal.go` — The Objective

- **`Goal`** struct: `ID`, `Objective`, `Phase`, `RoundsStarted`, `MaxGoalRounds`, `Blocker *Blocker`, `Revision`, `CreatedAt`, `UpdatedAt` — the durable half of D7, persisted with the session
- **`Phase`**: `active` | `paused` | `blocked` | `complete`, with `Valid()`; a goal is in exactly one, and the phase — not the conversation — is what a resumed session reports
- **`Blocker`**: `Code` + `Reason`; the codes are `round-limit`, `no-progress` and `model-reported`. Only a blocked goal carries one, and `Validate` enforces the pairing in both directions
- **`Ref`** (`{ID, Revision}`) and `(*Goal) Ref()`: every mutation echoes the exact view it was decided on, and an older revision is refused instead of overwriting newer state
- `Clone()` (deep, including the blocker), `RoundsLeft()`, `Validate()` (id, objective, known phase, positive cap, rounds inside the cap, revision ≥ 1, blocker pairing)
- `DefaultMaxRounds = 256` — the fallback cap; `Service.DefaultRounds` carries the configured one (`goal.max_goal_rounds`), so the `/goal` command and the goal tool cannot disagree. One round is a whole turn, so this bounds continuation, not the cost of one round

### `service.go` — State and Permission

- **`Service`** owns the current goal plus an **activation flag that is never persisted**. `Restore` disarms unconditionally, so a start, a resume and a fork all begin without permission to continue, and only a human can arm one
- `Snapshot() *Goal` (detached clone), `Armed() bool`, `Restore(*Goal) error` (validates, then disarms)
- Mutations, all compare-and-set on `{id, revision}` under the lock: `Create(objective, maxRounds)` refuses while an unfinished goal exists and arms the new one; `Edit`, `Pause`, `Resume` (rearms an active goal without touching the revision, refuses at the round cap), `Complete`, `Block(code, reason)`, `Clear`
- `StartRound() (int, error)` claims the next 1-based round, increments `RoundsStarted` and bumps the revision; the caller persists it before working, so a crash loop cannot spend the budget twice. `ShouldContinue() (bool, string)` is the read-only twin
- **`Error`**: a stable `Code` (`no-goal`, `unfinished-goal`, `stale-revision`, `illegal-phase`, `round-limit`, `disarmed`, `invalid`) plus a message for a human
- **`command.go`**: `ParseCommand` (the `/goal` grammar: a bare control word, `edit <objective>`, or an objective) and `Describe` (the lines `/goal` prints)
- **`driver.go`**: `State` (idle, running, compressing, dialog, input) and `Decide(g, armed, state) Decision` — pure, advisory, and the reason it returns is what makes a silent driver debuggable. `Service.StartRound` is the authority and re-checks under the same lock that claims the round
- **`prompt.go`**: `RoundPrompt(g, round)` — the `<goal_round>` block: the JSON-quoted objective, `Round: n/N`, the workspace-and-tool-results authority rule, the evidence rule for completion, and the goal tool's authority in the model's own terms
- The service never touches the filesystem: the TUI is the single writer, at its own safe points
- The user's page is [goals.md](goals.md): the commands, the authority table, every way a goal stops, what survives a restart, and the cost

---

### Session switching (`adoptSession`)

- One function swaps everything a session owns: the transcript, `Agent.History` (rebuilt with `historyFromSession`, so the model's context and the screen agree), the todo list (`Write(branch.Todos, false)` — replace, never merge, so an empty list clears), the goal (`Restore`, which disarms) and the per-session counters. `--resume`, `/session` and `/fork` all go through it, because the defect it replaces (#270) was a switch that replaced the screen and left the rest behind

### The continuation driver (`tui/goal_driver.go`)

- `goalGrace` (from `goal.idle_grace_seconds`, default 2 s) is the wait before an automatic round: long enough for someone already typing to get their message in, which matters because a run refuses concurrent input. `goalNoProgress` (from `goal.no_progress_rounds`, default 3) is the threshold `settleGoalRound` blocks on
- `beginAgentRun(prompt, goalRound)` is the one admission path for both a human message and a round; `goalRound` travels to `runAgent` as a value and becomes `types.WithGoalRound(ctx)`, which is what the goal tool reads to refuse `create`/`edit`/`pause`/`resume` inside a round
- `scheduleGoalRound` arms one `tea.Tick` only when `goal.Decide` allows it; the `goalRoundMsg` handler re-checks the same decision, so a human who started typing during the grace period wins
- `startGoalRound` claims `Service.StartRound` and **persists the counter before the work**, so a crash between the claim and the end of the round wastes at most one round
- `settleGoalRound(runErr)` runs at every terminal `StreamDone`: an errored or cancelled round pauses the goal (cancellation never auto-restarts), three consecutive tool-free rounds block it with `no-progress`, and spending the last round blocks it with `round-limit`. Every one of those appends a line to the transcript, so the user is told rather than left to type `/goal`

---

## Package: `skill`

### `skill.go`

- **`Skill`** struct: `Name`, `Description`, `Content`, `Builtin bool`, `Path string`
- **`SkillWithSource`** struct: `Skill`, `Source` ("builtin"/"user")
- **`Discover(cwd string) []Skill`** — 3-layer scan:
  1. Builtin (embedded via `//go:embed builtin/*.md`)
  2. Global (`~/.tinycode/skills/`)
  3. Project (`.tinycode/skills/` from cwd upward)
  - Later sources override earlier (dedup by name, reverse iteration)
- **`DiscoveredNames(cwd string) string`** — "name — description" lines for system prompt
- **`FindByName(name, cwd string) *Skill`**
- **`LoadContent(name, cwd string) string`**
- **`LoadOnce(name, cwd string) (string, bool)`** — dedup: loads once per session
- CRUD: `CreateOne(content)`, `EditOne(name, content)`, `DeleteOne(name)`, `ListAll(cwd)`
- **Builtin skills**: `code-review`, `git-commit` (in `skill/builtin/`)

---

## Package: `tui`

### `model.go`
- **`TuiModel`** struct — main Bubble Tea model with fields:
  - Agent/config: `agent`, `config`, `registry`, `provReg`
  - UI: `messages []chatMessage`, `vp viewport.Model`, `input textarea.Model`, `spinner`
  - Streaming: `status TuiStatus`, `streamCh chan tea.Msg`, `curAssistant *chatMessage`
  - Run lifecycle: `runMu`, `runID uint64`, `runActive bool`, `runCancel context.CancelFunc`, `runInterrupted bool`
  - Selection: `charSelStart/End selPos`, `lineSrcs []lineSrc`
  - Rendering: `grid *CellGrid`, `msgRowCount []int`, `msgDirty []bool`
  - Session: `SessionDir`, `currentBranch`, `sessionStore *session.Store`
  - History: `inputHistory []string`, `historyPos int`
  - Dialogs: `cmdPalette`, `dialogMode`, `quitConfirm`
- **`NewTUI(ag, cfg, reg, provReg, todoStore, resume ...) *TuiModel`**
- **`Button`** struct: `MsgIdx`, `Line`, `Col`, `Width`, `Label`, `Action func()`
- **`selPos`** struct: `MsgIdx`, `Field` ("content"/"reasoning"), `Offset int`
- **`lineSrc`** struct: `MsgIdx`, `SourceField`, `Text`, `CharStart`, `CharEnd`, `ContentOffset`

### `cellgrid.go`
- **`CellStyle`** struct: `Bold`, `Italic`, `Underline`, `Fg lipgloss.Color`, `Bg`, `Link string` (OSC 8 hyperlink target; runs only merge when equal)
- **`Cell`** struct: `Rune rune`, `Style CellStyle`, `Width int` (1 or 2 for CJK)
- **`CellChunk`** struct: `Text string`, `Style CellStyle`
- **`CellGrid`** — virtual framebuffer:
  - `NewCellGrid(width, height int) *CellGrid` — raises a non-positive dimension to one cell (`minTerminalWidth`), so a grid is never zero-area; `gridWidth(width)` exposes the same clamp for callers that compare against it (see `view.go`)
  - `Append(runes []rune, style)`, `AppendChunk(chunk)`, `AppendChunks(chunks)`, `AppendInline(chunks)`
  - `Render() string` — ANSI output, groups same-style runs; runs carrying `Style.Link` are wrapped by `hyperlink()`
  - `Fill(startRow, startCol, endRow, endCol, style)` — selection highlight
  - `ExtractText(startRow, startCol, endRow, endCol) string` — CJK-aware
  - `Reset()`, `RowCount() int`, `RowText(row) string`, `Get(row, col) Cell`
- `hyperlink(url, text) string` — OSC 8 wrapper; terminals without hyperlink support ignore it
- `wordWrap(text, maxWidth, style) []CellChunk` — preserves indent
- `styleCache` + RWMutex for CellStyle→lipgloss memoization

### `block.go` — Markdown Parsing
- **`ContentBlock`** struct: `Type` ("paragraph"/"heading"/"code"/"list"/"quote"/"hr"/"table"), `Chunks []TextChunk`, `Level`, `Language`, `Code`, `Items []ContentBlock`, `Numbered`, `Headers`, `Rows`
- **`TextChunk`** struct: `Text`, `Bold`, `Italic`, `Code`, `Link`
- **`parseMarkdown(md string) []ContentBlock`** — goldmark AST → ContentBlocks

### `components.go`
- **`MessageComponent`** interface: `Render(msg chatMessage, sel bool) []CellChunk`
- **`BlockComponent`** interface: `Render(block ContentBlock, sel bool) []CellChunk`
- Components: `UserComponent`, `SystemComponent`, `AssistantComponent`, `ReasoningComponent` (foldable [+]/[-]), `AnswerComponent`, `ToolCallComponent`, `ParagraphComponent`, `HeadingComponent`, `CodeComponent`, `ListComponent`, `QuoteComponent`, `HRComponent`, `TableComponent`, `ButtonComponent`
- `SystemComponent` renders `chatMessage.Banner` through `welcome.go` (colored, no `→` prefix); plain system messages keep the dim `→ ` prefix

### `messages.go`
- **`TuiStatus`** (int): `StatusIdle=0`, `StatusStreaming`, `StatusError`
- TUI messages: `StreamMsg`, `UsageMsg`, `CostMsg`, `RetryMsg`, `FailoverMsg` (a route change, shown as `⇄ routing a → b: …`), `StreamDone`, `ChatMsg`, `ToolCallMsg`, `ToolResultMsg`, `LSPDiagMsg`, `modeSwitchMsg`
- **`chatMessage`** (internal): `Role`, `Content`, `ReasoningContent`, `ReasoningFolded`, `ToolCalls []ToolCallInfo`, `Streaming`, `Blocks []ContentBlock`, `TodoSnapshot []tool.TodoItem`, `Banner *welcomeInfo`

### `welcome.go` — Startup Banner
- **`welcomeInfo`** struct: `Tools`, `Skills`, `Agents` — counters shown in the banner
- **`newWelcomeMessage(info) chatMessage`** — builds the start-up system message: `Content` is a plain-text fallback (copies), `Banner` selects the styled renderer
- **`renderWelcomeLines(info, width) [][]CellChunk`** — lays the banner out as one row per line: ASCII wordmark (dropped below `welcomeMinWidth`), counters, `Get started` shortcuts, config/source footer
- The Source row is an OSC 8 hyperlink (`welcomeSourceLink`, underlined, wordmark color) so it is clickable in supporting terminals; the Config row stays plain text
- Each row is placed with `AppendInline` (see `view.go`) so the art and column alignment are never word-wrapped; over-wide rows degrade to wrapped plain text
- `flattenWelcomeLines` collapses rows to one chunk per row for chunk-per-row callers

### `update.go`
- **`(*TuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd)`** — central event loop
- Handles: mouse (scroll, click, char-level selection), key presses, window resize, stream messages, tool calls, LSP diagnostics, todo updates, permission dialogs
- `tea.WindowSizeMsg` clamps every dimension to at least one cell (`minTerminalWidth`/`minTerminalHeight`) and never lets the viewport height or the input width go negative. A pty that was never given a window size reports `0 0`, and a zero-area `CellGrid` used to panic on its first `Append`; for a real terminal the clamp is a no-op, and the floor is one cell rather than a comfortable layout size so a genuinely narrow terminal is not laid out wider than the renderer's truncation width.
- 18 slash commands (19 names; `/exit` and `/quit` share one arm): `/exit`, `/quit`, `/compress`, `/dialog`, `/help`, `/verbose`, `/fork`, `/session`, `/sessions`, `/thinking`, `/model`, `/theme`, `/diagnostics`, `/sandbox`, `/undo`, `/goal`, `/skill`, `/plan`, `/build`. `/goal` takes the rest of the line: empty shows the goal, a bare `pause`/`resume`/`clear` performs it, `edit <objective>` replaces the objective, and anything else creates one — so `/goal pause after verification` is an objective, not a pause plus discarded text
- `notifyFinishedCmd(elapsed, what)` — a `tea.Cmd` (never an inline call: the desktop channel spawns a process) that announces a run which outlived `notify`'s threshold; `runStart` is stamped in `beginRun` and is what the elapsed time is measured from
- `beginRun()`/`finishRun(id)`/`cancelRun()` — one run at a time; Ctrl+C cancels the run's context (and any pending permission) while the status stays streaming until that run's terminal message; every stream message carries a `RunID` and superseded runs are dropped
- `runAgent(ctx, id, prompt)` — sets StreamCallbacks, runs agent in goroutine, stamps all messages with the run id
- `persistSession()` — the single save path for `/exit`, `/quit` and double-Ctrl+C; reuses `currentBranch` (preserving `AllowedPaths`) instead of minting a duplicate id
- `generateSessionTitleCmd()` — "title" agent call runs as a `tea.Cmd` (15s timeout) instead of blocking the event loop
- `lspDiagCmd()` reads `lsp.DiagnosticsSnapshot()` inside a `tea.Cmd` (never on the Update goroutine) and posts `LSPDiagMsg`; it is fired from the existing spinner tick (~10 Hz) and after every tool result, which is what feeds the status-bar `errors: N` counter and `/diagnostics`
- `/compress` refuses while a run is active (compression must not touch `History` concurrently) and runs the summarizer as a `tea.Cmd` instead of inline: on the Update goroutine a stalled provider froze the whole interface, and the Ctrl+C that would cancel it could never be delivered. `beginCompress`/`finishCompress` hold the compression lifecycle under `runMu` and `beginRun` refuses while it is held, so a run and a compression never touch `History` at once; Ctrl+C cancels the summarizer, and the result message carries the compressed/no-op/cancelled/failed outcome into the status bar. `sessionTokens`/`sessionToolCalls` are maintained from the stream and reset when the transcript is swapped; a `UsageMsg` from a call whose endpoint reported usage replaces the estimate charged to that call (`callTokens`) with the reported total, so the counter is provider-accurate where a provider reports and keeps the estimate where it does not; the same message adds its `CachedPromptTokens` to `sessionCached`, the session's reported cache reads
- `View()` tolerates an empty transcript with a dirty todo list, and a message role with no component renders zero rows so `msgRowCount`/`lineSrcs` stay in sync

### `view.go`
- **`(*TuiModel) View() string`** — full TUI layout
- Incremental rendering: dirty-message tracking, only re-renders from first dirty (~2.3ms)
- Banner messages (`msg.Banner != nil`) bypass word-wrap and place each pre-laid-out row inline
- `stripANSI` removes both CSI and OSC sequences, so OSC 8 hyperlinks never leak into selection/copy text
- Status bar: mode icon, model, spinner, provider, tokens, `goal n/max` (with `blocked`/`done` when it stops), spend, tool calls, msg count, diagnostics, duration, history. The goal sits above the spend in priority: at 80 columns the round survives and the money does not. `tokens:` is the session counter; when `budget.max_tokens_per_session` is configured it reads `spent/limit`, and with no budget configured it is byte-identical to what it has always been (which is what keeps the committed frame goldens stable). Reported cache reads are appended as `(N cached)` only when a provider reported one, so a route without caching renders the bar it always did (roadmap A4)
- Character-level selection via `grid.Fill()` with SelectionStyle

### Frame verification (test files)
Design reference and usage: [docs/tui-verification.md](docs/tui-verification.md); the
mechanism lives in the nested `tuiprobe` module, pinned in `go.mod`. The recipe for
adding a scenario — frame, black-box scenario file, or tool release — is in
[AGENTS.md](AGENTS.md).
- `scenarios_test.go` — eleven `frame*` functions and **one** scenario table (`name`,
  golden sizes, shot specs, builder) that drives frames, the ANSI frame, determinism,
  geometry, images and the live-stream rendering. The builders leave `sessionStart` at
  the zero time, so the status bar's session clock renders a constant (saturated)
  duration instead of the wall clock.
- `verification_test.go` — the assertions, each a few lines: `golden.Assert` (frames,
  byte for byte, `-update` aware), `golden.Deterministic`, `golden.Clip`/`FitsWidth`
  (geometry, mirroring what bubbletea truncates before writing), `tuiprobe/render/font`
  for PNGs, `tuiprobe/session` for `bin/tinycode` on a real 80x24 PTY, a second run on
  a size-less one, and a replay of the live stream into `tuiprobe/screen`.
- `tui/testdata/golden/frames/*.txt` — 29 plain-text frames (10 scenarios at 80x24 and
  120x40, plus 100x30 or 200x50 where a wider layout adds something, and 40x12 for the
  narrow end). `frames/ansi/markdown_80x24.ansi` — one raw ANSI frame, so colour, bold,
  underline and OSC 8 links stay diffable. Regenerate with
  `go test ./tui -run Golden -update`.
- `testhelpers_test.go` — the helpers other tests still need: the stream waiter and
  `lockedBuffer`, `frameDiff` (a wrapper over `golden.Diff`), and `withTrueColor`, which
  pins the lipgloss profile to TrueColor and empties `styleCache` for a render, because
  a non-TTY stdout otherwise renders pure ASCII and every style vanishes.
- The gated half (`TUI_SHOT=1`, `make test-tui-visual`) also runs the browser smoke.

### `theme.go`
- **`Theme`** struct: `Name` + 23 color fields (CellGrid + Lipgloss + welcome-banner colors)
- **`ThemeDefault`**, **`ThemeNord`**
- `ApplyTheme(t Theme)` — updates global styles (incl. `bannerArtStyle`/`bannerAccentStyle`/`bannerKeyStyle`), clears cache, triggers MarkAllDirty
- `ThemeNames() []string`, `LookupTheme(name string) *Theme`

---

## Package: `types`

### `types.go`
- **Constants**: `RoleSystem`, `RoleUser`, `RoleAssistant`, `RoleTool`
- **`Message`** struct: `Role`, `Content`, `Name`, `ToolCallID`, `ToolCalls []ToolCall`, `ReasoningContent`, `ReasoningSignature` (the vendor's receipt for a thinking block, carried from the response onto the assistant turn so Anthropic/Bedrock can replay it; empty for providers that do not sign reasoning)
- **`ToolCall`** struct: `ID`, `Name`, `Arguments string` (raw JSON)
- **`ToolDef`** struct: `Name`, `Description`, `Parameters map[string]any`
- **`ChatRequest`** struct: `Messages []Message`, `Tools []ToolDef`, `MaxTokens int`, `Model string`, `StreamCallbacks *StreamCallbacks`
- **`ChatResponse`** struct: `Content`, `ToolCalls []ToolCall`, `ReasoningContent`, `ReasoningSignature`, `Usage *Usage`, `Cost *Cost` (both nil when the endpoint reported none), `Route`/`Model` (the route and model that **served** the call, stamped by the provider, so a failed-over call is priced against the route that answered)
- **`Usage`** struct: `PromptTokens`, `CompletionTokens`, `TotalTokens`; `Add(v Usage) Usage` sums two records
- **`StreamCallbacks`** struct: `OnReasoningDelta`, `OnTextDelta`, `OnToolCall`, `OnToolResult`, `OnStepDone`, `OnUsage` (fired once per LLM call that reported usage), `OnCost` (fired once per call, including a call whose cost is unknown), `OnRetry` (fired before a request is re-sent, carrying the attempt and its cap so a caller renders `2/3` without knowing the policy), `OnFailover` (fired once per failed route before the next one is tried, carrying both route names and the failure)
- **`Usage`** detail fields: `CachedPromptTokens`, `CacheWriteTokens`, `ReasoningTokens` — one per *billing lane*, so OpenAI's `prompt_tokens_details` shape and DeepSeek's `prompt_cache_hit_tokens` land in the same field. They are subsets of the counts beside them, not extra tokens
- **`Cost`** struct: `Amount`, `Currency`; **`CostSource`** (`unknown`/`reported`/`declared`); **`CostEvent`**; **`CostTotals map[string]float64`** with `Add` and a `String()` ordered case-insensitively by unit — two routes billing in different units are accumulated apart, never added together
- **`Memory`** struct: `ID`, `Key`, `Value`, `Source`, `CreatedAt`
- **`MemoryStore`** interface: `Remember`, `Recall`, `Forget`, `List`; implementations `agent.LocalMemory` (default) and `agent.HTTPMemoryClient` (opt-in), both wired by `main.go` (see `agent/memory_local.go`)
- **`WithPlanWriteRestriction(ctx, bool)` / `PlanWriteRestricted(ctx)`** — plan-mode write restriction travels on the run context, so concurrent sub-agents cannot flip it for each other

---

## Package: `tlog`

### `tlog.go`
- **`Level`** (int): `LevelTrace=-1`, `LevelDebug=0`, `LevelInfo=1`, `LevelWarn=2`, `LevelError=3`
- **`Init(dir string, level Level)`** — creates timestamped log file
- **`ParseLevel(s string) Level`**
- **`Trace/Debug/Info/Warn/Error(service, msg string, kv ...any)`**
- Format: `LEVEL TIMESTAMP +ELAPSED service=name key=val... message`
- File-only output (no stdout), singleton with mutex

---

## Data Flow

```
User Input (textarea / CLI arg)
  → TUI: ChatMsg → Update() → runAgent() goroutine
  → agent.Run(ctx, prompt) — ReAct Loop
    → LLM Provider (streaming SSE via StreamCallbacks)
    → Tool execution (concurrent goroutines, permissions checked per-call)
    → No tool calls → final answer
  → StreamCallbacks → streamCh (buffered 200) → TUI Update()
  → TUI View() → CellGrid → viewport.SetContent() → terminal
```

## Key Design Decisions

1. **Custom CellGrid** — flat rune array with styles; no external markdown renderer
2. **Concurrent tool execution** — multiple tool calls per step run in goroutines + channel
3. **Permissions engine** — Ruleset with last-match-wins, wildcard `*`/`?` support
4. **MCP first-class** — native stdio/HTTP MCP client, no external SDK dependency
5. **SSRF protection** — one shared policy in `internal/netsafe`: resolve once, validate every address (refusals wrap `ErrBlocked` so callers can answer 403), pin the validated IP in `DialContext`, re-validate each redirect hop, optional per-client loopback allowance; used by `web_extract`, the browser pre-flight/interceptor, the browser filtering proxy and the MCP HTTP transport
6. **7 fuzzy edit strategies** — exact → trimmed → ws → indent → escape → unicode → block-anchor
7. **5-level web extract fallback** — HTTP → Cloudflare → Google Cache → Wayback → Chromium, all behind a shared SSRF client that pins the validated IP in `DialContext` and re-validates every redirect hop; before Chromium starts, the observable HTTP redirect chain is walked with the same client (page-level JS/meta redirects remain a residual risk)
8. **Session persistence** — JSON on disk (mode 0600), fork/branch support, AI-generated titles; ids and fork labels are charset-validated and contained inside the session directory
9. **Context compression** — Hermes-style head/middle/tail at 50% of context window
10. **3-layer skill discovery** — builtin (embedded) → global → project, with override
11. **Incremental TUI rendering** — msgDirty/msgRowCount tracking, ~2.3ms constant render time
12. **Security sandbox** — command blocklist, symlink-resolved path containment, FIFO permission queue (`once`/`session`/`always`), and a `recover()` guard so a panicking tool cannot kill the process

## Known Limits (accepted residual risk)

- **Sandbox check-vs-open race**: the file tools no longer check a path and then open it by name. `openSandboxed` (`tool/sandboxio.go`) opens the project root once and the file relative to it with `openat2(RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS)` (`tool/pathbeneath_linux.go`), so the descriptor that is read or written is the one the kernel verified: a component swapped for an escaping symlink after the permission answer fails the open with EXDEV instead of being followed. `CheckPath` still runs the same probe as an early check for callers that only need a verdict. What `openat2` cannot cover — a path the user explicitly allowed *outside* the root, platforms without the syscall (macOS) and kernels older than 5.6 — is opened through `openResolvedNoFollow` (`tool/pathbeneath_walk.go`): the resolved path is walked one component at a time with `O_NOFOLLOW` (`O_PATH` per directory on Linux, `O_RDONLY` on macOS), and only after re-resolving the path proves it still resolves to itself, so a component swapped for a symlink after the check is refused with `ELOOP` and a path that moved is refused with `errPathChanged` instead of being followed (issue #7 S1). `relBeneath` keeps `..` components so the probe resolves `link/..` the way the OS would, and the resolved form is used because `RESOLVE_BENEATH` rejects absolute symlinks outright. `writeSandboxed` replaces a file atomically (issue #36): the bytes go into a temp file in the target's own directory — created through the same sandbox-aware open, so it cannot land outside the root — and one rename moves it over the target, with the existing file's mode preserved and the temp removed on every failure path. Opening the target with O_TRUNC and writing into it destroyed the old content before the new bytes were durable, so an interrupted write left the file empty or half written. Residual: subsystems that do their own I/O — the headless browser, gopls and git — are outside this layer (issue #7 S3); a platform with no `openat` at all still opens by name; and an atomic replace changes the inode, so a process holding the file open keeps reading the old bytes (which is the point) and hard links to the old inode are not carried over.
- **bash process group**: on timeout the tool kills the process tree, the group, and — on Linux — the cgroup v2 group the command was started in. The tree is collected *before* the group signal, because killing the shell re-parents the survivors to init; a `setsid(2)` descendant leaves the group but keeps its parent, so it is still in the tree and no longer survives. Enumeration is native — `kern.proc.all` on darwin (`tool/procchildren_darwin.go`), `/proc/<pid>/status` on linux (`tool/procchildren_linux.go`), `ps` only as the fallback for other unix platforms — so no subprocess is spawned on the kill path and no `ps` binary is required. A descendant that double-forks (its immediate parent exits while the shell keeps running) re-parents to init and leaves the group, which process ancestry alone cannot follow; cgroup membership can, because it is inherited and cannot be given up (issue #9). `newBashCgroup` (`tool/cgroup_linux.go`) creates a child of this process's own cgroup — a systemd user session delegates that subtree — or of the mount root (a privileged container), and anything else returns `errNoBashCgroup` and the portable kill stays in charge; `bashCgroup.kill` writes `cgroup.kill` (Linux 5.14+) or signals every pid in `cgroup.procs`, which closes the same hole on older kernels. The tool result names the mechanism that fired (`killed with cgroup v2 + process group` or `killed with process group + tree walk`), so a reader can tell whether a detached process may have survived. On macOS and in any container without a writable delegation the documented limit stands, and `TestDoubleForkedDescendantEscapesTheTreeWalk` records it.
- **Chromium fallback**: both browser paths now point Chromium at a loopback filtering proxy (`internal/browserproxy`, `--proxy-server` + `--proxy-bypass-list=<-loopback>`), so every hostname the browser contacts — the initial navigation, each redirect hop, XHR/fetch, iframes, images, WebSocket upgrades and the `--dump-dom` load — is resolved exactly once, validated and pinned by this process; a tunnel or request whose host is private/loopback/metadata is refused with 403. Redirects are handed back to the browser rather than followed by the proxy, so the next hop is validated too, and QUIC is disabled because HTTP/3 would leave over UDP without asking the proxy. On top of that the rod path keeps request interception (`Browser.HijackRequests`) and both paths pin the top-level host with `--host-resolver-rules=MAP <host> <ip>`. The proxy is required, not best effort (issue #8): if it cannot start, or the launcher cannot start Chromium with the sandbox flags, both paths return `errBrowserSandboxUnavailable` instead of crawling with the weaker pre-flight/interceptor protection, and the flags close the channels that never ask a proxy — `--disable-quic` (HTTP/3 over UDP), `--force-webrtc-ip-handling-policy=disable_non_proxied_udp` (WebRTC/STUN candidates) and `--disable-background-networking` (Chromium's own traffic). The scoped loopback exemption the smoke test needs (`netsafe.AllowAuthority` through `browserproxy.Start`) is a test seam, not a runtime default: production grants no exemption.
- **MCP loopback**: a loopback-configured endpoint exempts exactly that authority (`netsafe.AllowAuthority`), so it cannot be redirected to another loopback port; every other SSRF rule still applies.
- **MCP**: requests are concurrent (one reader goroutine with per-id dispatch) and a cancelled call only unregisters itself, leaving the transport usable; `tool.CloseMCPServers()` (called from `main.go`) closes every client and reaps stdio children on exit. Server-initiated requests are answered (`ping` and `roots/list` with a result, anything else with a `-32601` error) so a server is never left waiting, and `serverInfo`/`tools` are mutex-guarded. The unmatched-message skip budget is tracked per pending call (`pendingCall.skipped`), so a request that never gets an answer fails on its own after `maxSkippedMessages` frames that answered nobody, leaving calls that are still within their budget — or already served — untouched.
- **`CheckCommand` and plan-mode checks** are advisory substring heuristics, not an OS-level boundary.

## Testing

- **1270 test functions + 10 fuzz targets** across the repository — what `go test ./... -count=1` runs for the main module, plus the evaluation corpora under `eval/` (nested modules the command still walks); each count below is listed with the command that produces it, and no count is carried in a copy that nothing checks
- `make fuzz` (`FUZZTIME=30s`) explores every fuzz target; `go test` already runs their seed corpora, so CI exercises them on every push
- `make test-browser` (`BROWSER_TEST=1`, `-run TestBrowserSmoke`) renders a JavaScript page through both browser paths against a loopback server and asserts the request carried the proxy's `Via` header, which proves the filtering proxy was used; `TestBrowserSmokeRefusesABlockedSubresource` then serves a page whose own authority is exempted from the loopback rule while the policy stays enforced, and asserts a subresource pointing at a *second, live* loopback service is refused (the script's `onerror` marker in the DOM) and that the service was never reached — a refusal is proven, not only that the proxy was used (issue #8). Both skip without a browser, so `make test` never launches one
- `make test-tui-visual` (`TUI_SHOT=1`) is the only place the TUI is looked at rather than asserted on: it renders the committed frame scenarios to PNGs through headless Chromium, starts `bin/tinycode` on a real 80x24 PTY (asserting the stream carried SGR styling and that the binary quits on the documented double Ctrl+C), runs it once more on a size-less PTY, and replays the live stream into a screen buffer that is screenshotted as `tinycode-pty-welcome-80x24.png`. Both tests skip without the variable, so `make test` needs neither a browser, nor a binary, nor a terminal device; PNGs land in `TUI_SHOT_DIR` (default `/tmp`)
- Sandbox I/O: the portable wrapper tests (read/create/truncate, mode, missing file, in-root symlink, outside-root and unconfigured fallbacks) run everywhere, and the replacement write is pinned by an injected mid-write failure proving the target keeps its bytes with no temp left behind (issue #36); the `openat2` containment cases (escaping symlink and directory link, `..` escape, in-root relative symlink, create) are linux-only and run in the CI job
- Statement coverage is **a snapshot, not a claim this document keeps** (issue #241). Take it as "where the suite is thin", not as a package's coverage today: the number moves with every PR, and it is not even stable between two runs of the same command on the same tree — `tool` measured 78.5% and then 78.4%. It is captioned rather than gated for that reason; the counts beside it are gated, because they are exact. Reproduce with `GOPROXY=off go test -count=1 -cover ./...` (`-coverprofile` invalidates the build cache and needs every dependency's source, so the module cache must be warm or the proxy off). **Snapshot at `dceda33`: skill 91.1%, tlog 89.6%, browserproxy 87.8%, session 87.0%, notify 86.2%, config 86.0%, agent 85.9%, netsafe 80.3%, mcp 80.1%, tui 78.5%, tool 78.4%, lsp 74.2%, root 67.3%, types 58.3%**
- `go test -race ./...` passes; the race detector is enforced in CI (`make test-race`)
- Agent loop: 13 integration tests using `MockLLM` step-by-step
- LSP: 36 tests — `io.Pipe`-based mock (no real server needed), single-reader correlation tests, server selection/error branches, baseline deltas, and `LSP_TEST=1` integration tests against real gopls
- MCP: 33 tests
- TUI: CellGrid roundtrip, keyboard, mouse, streaming, selection, todo rendering — plus, since the frame work landed, three layers that can actually see the frame:
  - `TestGoldenFrames` (29 plain-text frames: 10 scenarios at 80x24/120x40, plus 100x30/200x50 for wide layouts and 40x12 for the narrow end) and `TestGoldenFrameANSI` (one raw ANSI frame) in `tui/scenarios_test.go` + `tui/verification_test.go`, regenerated with `go test ./tui -run Golden -update`. Rendering is pinned to a TrueColor profile for the duration of a frame render and the `CellStyle` → lipgloss style cache is emptied, because without that lipgloss sees a non-TTY stdout and every style silently disappears. Each frame asserts it still carries SGR sequences, and every scenario is rendered twice and compared so the incremental grid path cannot drift.
  - The properties an in-process program driver used to cover — typing, the double Ctrl+C quit, a typed `/exit` — are asserted from *outside* the process now, by the scenario files (`tui/testdata/scenarios/*.scenario`) driven through `tuiprobe` on a real PTY, where `startup` and `exit-command` check the exit code. One property did not move with the others, and now does: **resizing a running program**. The scenario runner gained a `resize` step for it (`tuiprobe` v0.1.5, issue #214), and `resize-running.scenario` is its guard — a `mark`, then `resize --size 40x12`, then a `wait --text … --since` that can only match the redraw the resize caused, followed by `fit` and a `screenshot` whose pixel size is asserted against the new geometry. Removing the `resize` line from that scenario makes it fail, which is the control that keeps the assertion honest. `tui/geometry_test.go` continues to cover the model's answer to a `tea.WindowSizeMsg` (including `0x0`); the scenario covers the terminal's.

The runner's steps are `open`, `send`, `wait`, `mark`, `stable`, `sleep`, `golden`/`diff`, `fit`, `resize`, `screenshot`/`shot`, `wait-exit`, `close`, `expect-exit`.
  - `make test-tui-visual` (gated by `TUI_SHOT` in `tui/verification_test.go`) renders the scenarios to PNGs through `tuiprobe`'s renderer and runs the built binary on a PTY through `tuiprobe`'s session — twice: at 80x24 and on a PTY whose window size was never set (`0 0`), the regression guard for `tui/geometry_test.go`'s clamp. The replay that screenshots the *running renderer's* stream rather than `View()`'s output is `tuiprobe replay --stream`. The status bar's session clock is pinned by a zero `sessionStart` in the scenario builders — `time.Since(zero)` saturates — so goldens and screenshots show a constant duration instead of the wall clock.
- Edit: 14 tests covering 7 fuzzy strategies
- Apply patch: 9 tests + sandbox gate tests
- Sandbox: symlink escape, permission queue, "allow once" semantics, process-group kill
- SSRF: redirect blocking, DNS pinning, non-public IP ranges (network-free)

### Harness rules (tests that drive an external process)

The rules an agent or reviewer needs before touching a test that drives an external process — a PNG
render, a PTY session, the real binary. `AGENTS.md` carries the short form, this is the design record
they were derived from (the 5-minute `tui-visual` hang on `master`, run `36731349829`); the machinery
they constrain now lives in the `tuiprobe` module, whose own references are
[tuiprobe/README.md](tuiprobe/README.md) and [tuiprobe/docs/parity.md](tuiprobe/docs/parity.md), with
this repository's side in [docs/tui-verification.md](docs/tui-verification.md).

The rules survived the move even though the identifiers below did not, and they are what any new gated
harness has to satisfy:

- **One budget per stage, named in the failure.** `session.Stage(name, budget, fn)` (in `tuiprobe/session`) runs the step in its own goroutine and returns a `*StageError` carrying the stage's name, its budget and its elapsed time — `stage wait-for-banner exceeded its 5s budget after 5.001s` — with `ErrStageTimeout` separating "too slow" from "wrong". Without it a wedged CDP (Chrome DevTools Protocol) call reports nothing until `go test -timeout` kills the package, and the goroutine dump is the only evidence; `TestStageNamesTheStepThatRanOut` pins it and needs no terminal.
- **No panicking helpers inside a stage.** A panic in a stage's goroutine tears down the test binary before the deferred cleanup runs, and a cleanup that then waits on a process nobody closed blocks until the package timeout. Helpers return errors; the rule is stated in `tuiprobe/README.md` → Design rules.
- **Cleanup kills before it waits**, and bounds the wait. `Session.Close` (`tuiprobe/session`) and `Session.Close` (`tuiprobe/pty`) are where that happens now; the failure it prevents is a wait on a process that was never signalled.
- **A deadline belongs to one operation, not to a client.** A client-wide deadline installed at connect covers every later operation and expires mid-run on a loaded machine, which is how the eight-screenshot run of the deleted harness died. The provider layer learned the same lesson from the other side and now bounds the *silence* rather than the request: `ollamaIdleTimeout` (`agent/provider_ollama.go`) and `openAIIdleTimeout` (`agent/provider_openai.go`) reset on every line, with a named watchdog on the non-streaming path (`openAIRequestTimeout`). A whole-request timer there would cut a healthy long generation (issue #185).
- **Timing knobs are injectable.** Every budget is a parameter — `Stage`, `WaitText`, `WaitStable` all take one — and a scenario carries its own `--timeout` per step, so a gated check can be starved on demand instead of only on CI.
- **The golden frame is the assertion; the PNG supports it.** A screenshot is evidence only when its dimensions are pinned to the geometry it claims to show: a full-page capture of an unclipped frame widens to its longest line (the compression status line made both the 40- and the 80-column scenario 1912 px wide — issue #25), so the same scenario's golden and PNG come from one builder (`shotSpec` in `tui/scenarios_test.go`), the page is clipped with the renderer's own truncation, and the artifact's size is asserted (`golden.Clip`/`FitsWidth`).

### How these numbers are measured

Counts in this document are produced by these commands at the commit they describe; if one changes, change the command's output here in the same PR:

```bash
find . -name '*_test.go' -not -path './tuiprobe/*' -print0 \
  | xargs -0 grep -h '^func Test' | wc -l              # 1270 test functions (main module)
grep -rn '^func Fuzz' --include=*_test.go . | wc -l    # 10 fuzz targets
ls tui/testdata/golden/frames/*.txt | wc -l            # 29 plain-text frames
ls tui/testdata/golden/ansi/*.ansi | wc -l             # 1 raw ANSI frame
```

A **test-count badge** used to be part of this set, and it was removed in issue #170 rather than
updated once more. It was a static shields.io number with no code path keeping it honest: it read 564
while this document said 667, then 710 while the tree had 857. The lesson is not "update it too" —
it is that a figure nothing can check will drift, so the measurement lives in the command above and
in whatever the badge would have duplicated. A badge that is computed from the tree at build time
would be a different thing, and nobody is building one.

These drifted by eight (654 documented, 662 actual) before the rule existed, which is why the
command sits next to the number rather than in a reviewer's head.

### Running the CI jobs locally

The gated jobs are reproducible off CI; each command below is the one the job runs, with the
environment it needs:

- **`ci`** (`make test`, `make test-race`, `make test-repeat`): everything runs everywhere except
  the Linux-only cases — the `openat2` containment tests and the cgroup reaping — which need a
  Linux host. `docker run --rm -v "$PWD":/w -w /w golang:1.27 go test ./tool/` is the short path
  (a running daemon is required; the kernel inside needs ≥ 5.6 for `openat2`). These have been
  green in every `ci` run recorded here; the container command itself was **not** run in this
  session, since no daemon was up.
- **`lsp`**: `make install-gopls` and `make install-tsls` (TypeScript 5, optional — the TypeScript
  case skips with the reason without it), then `make test-lsp` (`LSP_TEST=1`). The target is
  verbose on purpose: which server was exercised, and which case skipped, is its point.
- **`browser`**: `make test-browser` (`BROWSER_TEST=1`). The smoke tests resolve the binary per
  path, exactly as the extractor does: the `--dump-dom` half uses `findExecBrowser` (Playwright
  headless shell first — the full macOS Chrome for Testing build hangs in `--dump-dom`, issue
  #43) and the `rod` half `findBrowser` (full browser first).
- **`tui-visual`**: `make test-tui-visual` (`TUI_SHOT=1`), needs Chromium, `/dev/ptmx` and a fresh
  `bin/tinycode`; `TUI_SHOT_DIR` decides where the PNGs land (default `/tmp`).
- **`cross`** / **`staticcheck`**: `GOOS=linux GOARCH=arm64 go build ./... && go vet ./...` and
  `make staticcheck`. Both are fully local.

A browser or PTY target that cannot start inside a restricted sandbox is expected to fail there —
that is what the `browser` and `tui-visual` jobs are for, and why every such test skips with a
reason instead of reporting a false pass.

## Modules

The repository carries **two Go modules**:

- the root module `github.com/yusiwen/TinyCode` — the agent, TUI, tools and their tests;
- `tuiprobe/` — `github.com/yusiwen/TinyCode/tuiprobe`, a language-agnostic CLI plus
  importable library that turns what a TUI really draws into artifacts a person, a
  test or an agent can read (screen text/ANSI/HTML, golden files, later screenshots
  and PTY-driven sessions). It exists so the TUI verification harness can be used by
  other projects instead of being re-written per project.

**A nested module is invisible to the root's `./...`** — measured: `go list ./...`,
`go build ./...` and `go vet ./...` from the root all skip it and exit 0, so a break
there would ship silently. `gofmt -l .` from the root does cover it. CI therefore
runs `make test-tuiprobe` / `make test-tuiprobe-race` in the `ci` job, the nested
build and vet in the `cross` matrix, and `make lint-tuiprobe` in the `staticcheck`
job; `.github/workflows/release-tuiprobe.yml` publishes it from `tuiprobe/v*` tags.
A new dependency in the nested module must be tidied **with the checksum database
reachable** (`go mod tidy` in `tuiprobe/`) and the result cross-built for the CI
platforms: on 2026-10-02 a `go get` with `GOSUMDB=off` produced a `go.sum` that was
complete on darwin/arm64 and missing the entries linux/amd64 needs for
`golang.org/x/image/vector`, which broke three jobs at once.
See `tuiprobe/docs/parity.md` for what it covers and `tuiprobe/docs/roadmap.md` for
what is still missing.

## Build & Run

```bash
make build          # → bin/tinycode (CGO_ENABLED=0, stripped)
make test           # go test ./... -count=1
make test-race      # go test -race ./... -count=1
make test-repeat    # go test ./... -count=3 (catches leaked global state)
make test-lsp       # LSP integration tests; needs gopls on PATH (nix develop provides it)
make install-gopls  # go install gopls@$(GOPLS_VERSION)
make test-browser   # real-browser smoke test (BROWSER_TEST=1); needs Chromium
make test-tui-visual # TUI screenshots + PTY smoke (TUI_SHOT=1); needs Chromium and a PTY
make lint           # go vet (blocking)
make staticcheck    # staticcheck, pinned via STATICCHECK_VERSION (v0.8.1)
make fmt-check      # fail when a tracked Go file is not gofmt-clean
make run PROMPT="..."  # one-shot mode
./bin/tinycode      # TUI mode
./bin/tinycode --list-sessions
./bin/tinycode --resume=TUI-20260607-235959
```

## Dependencies

| Package | Purpose |
|---------|---------|
| `bubbletea` + `bubbles` | TUI framework (viewport, textarea, spinner) |
| `lipgloss` | ANSI styling |
| `go-runewidth` | CJK character width calculation |
| `go-openai` | LLM provider types |
| `goldmark` | Markdown AST parsing |
| `cobra` | CLI flag handling |
| `go-rod` | Headless Chromium (web_extract fallback) |
| `godotenv` | .env file loading |
| `golang.org/x/net` | HTTP utilities |

## CI

- `ci` job: build, `gofmt` gate, `go vet`, tests, `-race`, and a repeated (`-count=3`) run, on Go 1.27.
- `release` job (`release.yml`, on tag push): cross-compiles the three archives via `make releases` on the same Go line, so the `go 1.27` directive is satisfied.
- `browser` job: installs Chrome for Testing (`browser-actions/setup-chrome`, `id: setup-chrome`) and runs `make test-browser` with `CHROME_PATH` set from the action's `chrome-path` output, so discovery does not have to pick between that browser and the runner image's unusable `chromium-browser` shim. This is the only place the real-browser paths are exercised.
- `tui-visual` job: installs Chrome for Testing the same way and runs `make test-tui-visual` (`TUI_SHOT=1`), the only place the built binary is started on a PTY and the frames are rendered to PNG. The golden frames and the headless program driver run in the `ci` job, which needs neither a browser nor a terminal device.
- `lsp` job: installs gopls at the Makefile's pinned `GOPLS_VERSION` (v0.23.0, matching the flake) plus `typescript-language-server` with TypeScript 5 (`make install-tsls`, non-fatal: the TypeScript case skips with the reason if the registry is unavailable) and runs `make test-lsp` (`LSP_TEST=1`) so the integration tests that spawn a real language server actually run. Installed-server coverage, per language: **go** (gopls) integration-tested in every `lsp` job run; **typescript/javascript** integration-tested when the server is installed (CI installs it) and otherwise covered ungated by a stand-in server that proves selection, arguments, `languageId`, the document URI and the diagnostics round trip; **python** has the "binary missing" path tested, pyright itself is not installed anywhere; **rust**, **cpp** and **java** have no server-level coverage — their configs are pinned by unit tests only.
- `cross` job: `GOOS/GOARCH` build + vet for linux/amd64, linux/arm64 and darwin/arm64 — this is also what type-checks the platform-only files (`tool/pathbeneath_linux.go`, `tool/sysproc_unix.go`, `tool/confinement_linux.go`, `tool/confinement_darwin.go`). It compiles the macOS launcher and does not run it; `TestSeatbeltBoundaryIsEnforced` on a Mac is the execution check.
- `staticcheck` job: blocking, pinned to `honnef.co/go/tools v0.8.1` via `make staticcheck` so a new release cannot red the build without a code change (bump `STATICCHECK_VERSION` in the Makefile to move it).
- Toolchain: CI, the Nix flake (`pkgs.go_1_27`) and the `go 1.27` directive in `go.mod` are all on the 1.27 line, so `gofmt`/`go vet` behave identically in every environment.
- **Annotation baseline, and why the gate is "no new annotations" rather than zero:** every job carries one `ubuntu-latest` migration notice from the runner image, and `browser` + `tui-visual` additionally carry `browser-actions/setup-chrome@v1`'s Node 20 deprecation warning, because the action targets Node 20 while the runner forces Node 24. Both are platform notices no change in this repository can remove, so a PR is clean when its counts match the baseline (verified on master `a4f815b` and on the previous green run):

  ```bash
  gh api "repos/$(gh repo view --json nameWithOwner -q .nameWithOwner)/commits/$SHA/check-runs?per_page=50" \
    --jq '.check_runs[] | "\(.name): \(.conclusion) annotations=\(.output.annotations_count)"'
  ```

## Dev Environment

- Nix flake (`flake.nix`) + `direnv` (`.envrc`) for reproducible toolchain
- Go 1.27 (`pkgs.go_1_27`), gopls 0.23.0, gofumpt pinned
- The dev shell's `go` is a wrapper that drops an inherited `GOROOT`/`GOTOOLDIR` before exec'ing the flake's Go: a host version manager (e.g. mise) re-exports `GOROOT` on every prompt, and direnv does not undo that, which would otherwise pair the flake's `go` with another release's compiler and fail every build with `compile: version … does not match go tool version …`
- `nix develop` or `direnv allow` to activate; the shell provides `gopls`, so `make test-lsp` works there without touching the host environment
- CI: GitHub Actions (build+lint+test on push/PR, cross-compile+release on tags)
