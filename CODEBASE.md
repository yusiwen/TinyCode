# TinyCode — CODEBASE Map

> AI coding agent in pure Go. Single binary, Bubble Tea TUI, ReAct agent loop, 24 built-in tools + MCP, LSP diagnostics, session persistence. 501 test functions, race-detector clean.

## Quick Reference

| File / Dir | Purpose |
|------------|---------|
| `main.go` | Cobra CLI entry, wires all packages together |
| `agent/` | ReAct loop, LLM providers, context compression, agent registry, permissions |
| `config/` | JSON config loading (defaults → user global → project local → env/CLI) |
| `internal/netsafe/` | Shared SSRF policy: blocked-IP table, resolve-once + pinned-IP HTTP client, redirect re-validation, optional loopback allowance |
| `lsp/` | LSP client (JSON-RPC over stdio), 4 tool wrappers, diagnostics |
| `mcp/` | Native MCP client (stdio + HTTP transports), JSON-RPC 2.0 |
| `session/` | Session persistence (JSON on disk), fork/branch, export, search |
| `skill/` | SKILL.md 3-layer discovery (builtin → global → project) |
| `tlog/` | Structured file logger with level filtering |
| `tool/` | 24 tool implementations (bash, edit, git, web, LSP, task, todo, sandbox, MCP, skill) |
| `tui/` | Bubble Tea TUI (CellGrid, viewport, markdown, selection, command palette) |
| `types/` | Shared types (Message, ToolCall, ChatRequest, StreamCallbacks, MemoryStore) |

---

## Package: `agent`

### `agent.go` — Core ReAct Loop

- **`Agent`** struct — fields:
  - `Config *AgentConfig` — mode config (plan/build/subagent)
  - `Provider LLMProvider`
  - `Tools []Tool`
  - `Memory types.MemoryStore`
  - `MemoryMode int` — 0=none, 1=auto, 2=on-demand
  - `SessionStore` — interface `{Append(msg types.Message) error; Flush() error}`
  - `History []types.Message` — multi-turn conversation
  - `CompressionThreshold int` (default 500000)
  - `ContextLength int` (default 1000000)
  - `discoveredCtxLen int` (unexported; lowered after `context_length_exceeded`)
  - `TodoStorer` — interface `{FormatForInjection() string}`
  - `SystemPrompt string`, `MaxSteps int`, `MaxTokens int`
  - `Verbose bool`, `ShowThinking bool`
  - `StreamCallbacks *types.StreamCallbacks`
  - `ContentStreamed bool`
- **`New(provider LLMProvider) *Agent`** — defaults: MaxSteps=20, MaxTokens=4096
- **`(*Agent) AddTool(t Tool)`** — registers a tool
- **`(*Agent) Run(ctx context.Context, prompt string) (string, error)`** — core ReAct loop:
  1. Build messages: system prompt + compressed history + memory (if auto) + user prompt
  2. Loop up to `maxSteps`:
     - Filter tools by `ToolAllowedFor`
     - Call `Provider.Chat()` with streaming callbacks
     - No tool calls → save to history + session, return content
     - Empty response (no content, no tool calls) → append placeholder, retry
     - Has tool calls → append assistant message with all calls, execute concurrently via goroutines, collect results in index order
     - Security block detection: result starting with `[SECURITY BLOCKED]` bypasses LLM
     - Fire `OnStepDone` callback after all tools complete
  3. Max steps reached: inject forced summary user message, one final LLM call with no tools
- **`(*Agent) CompressHistory() bool`** — compresses `a.History` in-place using provider
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
- **`Tool`** struct: `Name`, `Description`, `Parameters map[string]any`, `Execute func(ctx, args) (string, error)`
- **`MockProvider`** — test double with injectable `ChatFunc`

### `provider_openai.go` — OpenAI-Compatible Provider

- **`OpenAIProvider`** struct (unexported fields: client, model, apiKey, baseURL)
- **`NewOpenAIProvider(apiKey, baseURL, model string) *OpenAIProvider`**
- `Chat()` — SSE streaming via `net/http` (not go-openai SDK's streaming); accumulates reasoning/content deltas and tool calls by index; 120s timeout; 64KB→256KB scanner buffer; `stream_options: {include_usage: true}`

### `provider_ollama.go` — Ollama Provider

- Build tag: `//go:build !no_ollama`
- **`OllamaProvider`** struct (unexported: baseURL, model, http client)
- **`NewOllamaProvider(baseURL, model string) *OllamaProvider`** — default baseURL: `http://localhost:11434`
- `Chat()` — line-delimited JSON (not SSE); tool results mapped to `role: "user"`; `thinking` field for reasoning

### `compression.go` — Context Compression

- **`EstimateTokens(text string) int`** — `len(text) / 4`
- **`EstimateMessagesTokens(msgs []types.Message) int`**
- **`ParseContextLimitFromError(errMsg string) int`** — regex extraction from API error
- **`(*Agent) compressHistory(ctx, messages) ([]types.Message, error)`** — algorithm (the head cut is snapped forward to a message-group boundary via `groupEnd`, so an assistant `tool_calls` message is never separated from its tool results):
  1. If tokens < threshold → no-op
  2. Need ≥4 user messages
  3. Head: first 2 user-message boundaries (+3 context after)
  4. Tail: last 2 user-message boundaries
  5. Middle → LLM summarization → `[COMPRESSED HISTORY]` system message
  6. Tool output in middle truncated to 200 chars during serialization
  7. Active TODO items re-injected after compression
- **`(*Agent) HandleContextError(err error) bool`** — detects `context_length_exceeded`, lowers `discoveredCtxLen` (5 regex patterns, valid range 1024–10M, only lowers never raises)

### `ruleset.go` — Permission Rules

- **`Effect`** (string): `EffectAllow = "allow"`, `EffectDeny = "deny"`
- **`Rule`** struct: `Action string` (tool name or `*`), `Resource string` (pattern or `*`), `Effect Effect`
- **`Ruleset`** = `[]Rule`
- **`Evaluate(action, resource string, rules ...Rule) Effect`** — last-match-wins, default Allow
- **`FilterTools(allTools []string, rules ...Rule) []string`** — filters out denied tools
- **`TranslateToolLists(base Ruleset, allowed, denied []string) Ruleset`** — converts config `allowed_tools` (whitelist) / `denied_tools` (additive) into rules, since the ruleset takes precedence
- `wildcardMatch(pattern, value string) bool` — `*` (any sequence) and `?` (single char) glob

### `memory.go` — Long-term Memory

- **`HTTPMemoryClient`** struct: `BaseURL string`
- **`NewHTTPMemoryClient(baseURL string) *HTTPMemoryClient`**
- Methods: `Remember(key, value string) error`, `Recall(query string, limit int) ([]types.Memory, error)`, `Forget(key string) error`, `List() ([]types.Memory, error)`
- Hindsight API: `https://memory.yusiwen.cn/dp-api`, bank "hermes", 15s timeout, auth via `HINDSIGHT_API_KEY` or `OPENAI_API_KEY`

### `truncate.go` — Output Truncation

- **`TruncationResult`** struct: `Content string` (preview + hint), `FullPath string`
- **`TruncateOutput(output string) TruncationResult`** — limits come from `TruncMaxLines`/`TruncMaxBytes`/`truncDir`, overridable at startup with `SetTruncationConfig(maxLines, maxBytes, outputDir)`; the saved full output is written 0600 because tool output can contain secrets
- Constants: `TruncMaxLines = 2000`, `TruncMaxBytes = 200KB`, `truncDir = "/tmp/tinycode/truncated"`
- Saves full output to file, returns head preview with `[TRUNCATED]` hint

---

## Package: `config`

### `config.go`

- **`Config`** struct (top-level):
  - `DefaultMode string`, `ShowThinking *bool`, `Verbose *bool`
  - `Providers []ProviderRecordConfig`
  - `Truncation *TruncationConfig`
  - `Agents map[string]AgentOverride`
  - `Sandbox *SandboxConfig`
  - `Theme string`, `SessionDir string`
  - `LSP *LSPConfig`, `LogLevel string`
  - `ContextLength int` (default 1M), `CompressionThreshold int` (default 500K)
  - `SearXNGURL string`, `MCPServers []MCPServerConfig`
- **`ProviderRecordConfig`**: `Name`, `Type` ("openai"|"ollama"), `Model`, `BaseURL`, `APIKeyEnv`
  - `APIKey()` method: priority → `api_key_env` → `UPPER(NAME)_API_KEY` → `OPENAI_API_KEY`
- **`AgentOverride`**: `MaxSteps`, `AllowedTools`, `DeniedTools`, `SystemPrompt`, `Model`, `Permissions []AgentRule` (an explicit ruleset that replaces the agent's built-in one and wins over the tool lists)
- **`AgentRule`**: `Action`, `Resource`, `Effect` (`"allow"`/`"deny"`) — a config-local mirror of `agent.Rule`
- **`SandboxConfig`**: `ProjectRoot`, `DenyCommands []string`, `AllowedPaths []string`
- **`MCPServerConfig`**: `Name`, `Transport` ("stdio"|"http"), `Command`, `Args`, `Env`, `URL`, `Headers`
- **`LSPConfig`**: `Enabled bool`
- **`TruncationConfig`**: `MaxLines`, `MaxBytes`, `OutputDir`
- **`DefaultConfig() Config`** — DeepSeek V4 Flash, `https://api.deepseek.com`, 1M context
- **`LoadConfig() Config`** — merge order: defaults → `~/.tinycode/config.json` → `./.tinycode/config.json`; `merge` covers every section (providers, agents, sandbox, mcp_servers, theme, searxng_url, truncation, LSP, context limits); a malformed file is reported on stderr and the defaults are used
- **`AddAllowedPath(path) error`** — patches only `sandbox.allowed_paths` in the *user* config (preserving unknown keys and never materialising defaults); used by "Always allow"
- **`LoadUserConfig() Config`** — defaults + user-global file only (no project layer); used when persisting settings so repo-controlled providers/system prompts are never promoted to the global config
- **`(cfg Config) Save() error`** — creates `~/.tinycode/` if needed and persists `config.json` with mode 0600

---

## Package: `tool`

### Tool Registration Pattern
Each tool exports a factory function returning `agent.Tool` with `Name`, `Description`, `Parameters` (JSON Schema), `Execute(ctx, args) (string, error)`.

### Complete Tool Name Registry

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
| `task` | sub-agent | `task.go` |
| `task_collect` | sub-agent | `task.go` |
| `todo` | task mgmt | `todo.go` |
| `sandbox_allow` | security | `sandbox.go` |
| `load_skill` | skill | `load_skill.go` |
| `skill_manage` | skill | `skill_manage.go` |
| `lsp_definition` | LSP | `lsp/tools.go` |
| `lsp_references` | LSP | `lsp/tools.go` |
| `lsp_hover` | LSP | `lsp/tools.go` |
| `lsp_symbols` | LSP | `lsp/tools.go` |
| `mcp_*` | MCP (dynamic) | `mcp.go` |

### Key Tool Implementations

- **`bash`**: Shell execution with plan-mode write blocking (mkdir, rm, mv, cp, heredoc, file redirect; reads the restriction from the run context) + sandbox command blocklist; runs in its own process group (timeout kills the whole tree, `WaitDelay` guards the pipes) and caps each stream at 1 MiB
- **`read_file`**: 2000-line limit, offset/limit paging, LSP warmup fire-and-forget, sandbox path check
- **`write_file`**: Creates parent dirs, LSP baseline + diagnostics
- **`search_files`**: path-sandbox gated (searching reads file contents); priority `rg` → `grep` → Go native `filepath.Walk`
- **`edit`**: 7 fuzzy strategies (exact → line-trimmed → ws-normalized → indent-flexible → escape-normalized → unicode-normalized → block-anchor with Levenshtein ≥ 0.65) + indentation correction
- **`apply_patch`**: V4A format (`*** Begin Patch / *** Update File: / *** Add File: / *** Delete File: / *** End Patch`), 3 phases: parse → validate → apply; every target path passes the shared sandbox gate before any I/O
- **`web_search`**: DuckDuckGo Lite (zero config) + optional SearXNG fallback (`SetSearXNG(baseURL)`)
- **`web_extract`**: 5-level fallback (HTTP → Cloudflare → Google Cache → Wayback → Chromium), SSRF protection via `internal/netsafe`, LLM summarization for >5000 chars (`SetSummarizer(fn)`)
- **`task`**: Sub-agent delegation (explore/general), sync or background mode, 120s timeout; a timed-out or cancelled sync task cancels the sub-agent's context (the result channel is buffered so the goroutine always exits)
- **`todo`**: CRUD with `TodoStore` (max 256 items, 4000 chars/item, one in_progress); every method takes an `RWMutex` and `Read` returns a copy
- **`sandbox_allow`**: Interactive permission dialog (Allow once / Allow session / Always allow / Deny)

### `sandbox.go` — Security Sandbox

- **`SandboxConfig`**: `ProjectRoot`, `DenyCommands []string`, `AutoAllowPaths []string`, `allowedPaths map[string]bool` (mutex-guarded)
- **`DefaultSandbox`** — global with deny: `rm -rf /`, `sudo`, `dd`, `mkfs`, fork bomb, etc.
- **`AccessDenied`** error: `Path`, `Message`, `DenyHint() string`
- **`CheckCommand(cmd string) error`** — blocklist matching (substring match; not a security boundary)
- **`CheckPath(absPath string) error`** — kernel-order symlink resolution (`resolveRealPath`, applied to the raw path before any lexical clean, so `link/..` cannot escape) + project-root containment + auto-allow + cached allows
- **`CheckPathAccess(ctx, path) (string, error)`** — the shared gate used by `read_file`, `write_file`, `edit`, `apply_patch`, `search_files`; returns a user-facing denial message or ("", nil)
- **`WithPathGate(t agent.Tool) agent.Tool`** — wraps a tool implemented in another package (the `lsp_*` tools) so its `path`/`file_path` argument goes through the same gate
- **`RequestPermission(ctx, path) (bool, string)`** — enqueues a FIFO request and blocks on its own channel until the TUI resolves it or ctx is cancelled
- **`SetAgentLabel(label string)`** — sub-agent label for dialog display
- **`ResolvePermissionByID(id, allow, mode) bool`** — the safe dialog API: answers the request the user was shown even if the queue head changed; **`ResolvePermission(path, allow, mode) bool`** — path-based variant (empty path = head) used by `sandbox_allow` and tests; `once` is never cached, `session`/`always` populate `allowedPaths`
- **`HasPendingPermission()`, `PendingPermissionPath()`, `CancelPendingPermission()`** — display/teardown helpers

### `bgtask.go` — Background Task Manager

- **`TaskState`** (int): `TaskRunning`, `TaskDone`, `TaskFailed`, `TaskTimedOut`
- **`BgTask`**: `ID`, `Agent`, `Goal`, `State`, `Result`, `Error`, `Done chan`
- **`BackgroundTaskManager`**: `NewBackgroundTaskManager()`, `Start(deps, name, goal) string`, `Collect(taskID) (string, error)`, `CollectContext(ctx, taskID)`, `Status(taskID) string`; all state writes go through a locked `finalize` (idempotent, `sync.Once` for `Done`) and a watchdog settles `TaskTimedOut` at the deadline so `Collect` cannot hang

---

## Package: `lsp`

### `client.go`
- **`Client`** struct: wraps `*Conn`
- **`NewClient(conn *Conn) *Client`**
- Methods: `Initialize(rootURI) error`, `Shutdown() error`, `GoToDefinition(uri, line, char) (*Location, error)`, `FindReferences(uri, line, char) ([]Location, error)`, `Hover(uri, line, char) (*Hover, error)`, `DocumentSymbols(uri) ([]SymbolInformation, error)`, `Diagnostics(uri, content) ([]Diagnostic, error)` (5s timeout)
- **`Diagnostic`** struct: `Range`, `Severity`, `Message`
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

- **`Session`** struct: `ID`, `Title`, `Preview`, `ModelName`, `CreatedAt`, `UpdatedAt`, `MessageCount`, `ParentSessionID`, `ForkAt int`, `AllowedPaths []string`, `Messages []types.Message`, `dir string`
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
- **`CellStyle`** struct: `Bold`, `Italic`, `Underline`, `Fg lipgloss.Color`, `Bg`
- **`Cell`** struct: `Rune rune`, `Style CellStyle`, `Width int` (1 or 2 for CJK)
- **`CellChunk`** struct: `Text string`, `Style CellStyle`
- **`CellGrid`** — virtual framebuffer:
  - `NewCellGrid(width, height int) *CellGrid`
  - `Append(runes []rune, style)`, `AppendChunk(chunk)`, `AppendChunks(chunks)`, `AppendInline(chunks)`
  - `Render() string` — ANSI output, groups same-style runs
  - `Fill(startRow, startCol, endRow, endCol, style)` — selection highlight
  - `ExtractText(startRow, startCol, endRow, endCol) string` — CJK-aware
  - `Reset()`, `RowCount() int`, `RowText(row) string`, `Get(row, col) Cell`
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

### `messages.go`
- **`TuiStatus`** (int): `StatusIdle=0`, `StatusStreaming`, `StatusError`
- TUI messages: `StreamMsg`, `StreamDone`, `ChatMsg`, `ToolCallMsg`, `ToolResultMsg`, `LSPDiagMsg`, `modeSwitchMsg`
- **`chatMessage`** (internal): `Role`, `Content`, `ReasoningContent`, `ReasoningFolded`, `ToolCalls []ToolCallInfo`, `Streaming`, `Blocks []ContentBlock`, `TodoSnapshot []tool.TodoItem`

### `update.go`
- **`(*TuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd)`** — central event loop
- Handles: mouse (scroll, click, char-level selection), key presses, window resize, stream messages, tool calls, LSP diagnostics, todo updates, permission dialogs
- 15+ slash commands: `/exit`, `/compress`, `/help`, `/verbose`, `/fork`, `/session`, `/thinking`, `/model`, `/sessions`, `/theme`, `/diagnostics`, `/skill`, `/plan`, `/build`, `/dialog`
- `beginRun()`/`finishRun(id)`/`cancelRun()` — one run at a time; Ctrl+C cancels the run's context (and any pending permission) while the status stays streaming until that run's terminal message; every stream message carries a `RunID` and superseded runs are dropped
- `runAgent(ctx, id, prompt)` — sets StreamCallbacks, runs agent in goroutine, stamps all messages with the run id
- `persistSession()` — the single save path for `/exit`, `/quit` and double-Ctrl+C; reuses `currentBranch` (preserving `AllowedPaths`) instead of minting a duplicate id
- `generateSessionTitleCmd()` — "title" agent call runs as a `tea.Cmd` (15s timeout) instead of blocking the event loop
- `lspDiagCmd()` reads `lsp.DiagnosticsSnapshot()` inside a `tea.Cmd` (never on the Update goroutine) and posts `LSPDiagMsg`; it is fired from the existing spinner tick (~10 Hz) and after every tool result, which is what feeds the status-bar `errors: N` counter and `/diagnostics`
- `/compress` refuses while a run is active (compression must not touch `History` concurrently); `sessionTokens`/`sessionToolCalls` are maintained from the stream and reset when the transcript is swapped
- `View()` tolerates an empty transcript with a dirty todo list, and a message role with no component renders zero rows so `msgRowCount`/`lineSrcs` stay in sync

### `view.go`
- **`(*TuiModel) View() string`** — full TUI layout
- Incremental rendering: dirty-message tracking, only re-renders from first dirty (~2.3ms)
- Status bar: mode icon, model, spinner, provider, tokens, tool calls, msg count, diagnostics, duration, history
- Character-level selection via `grid.Fill()` with SelectionStyle

### `theme.go`
- **`Theme`** struct: `Name` + 20 color fields (CellGrid + Lipgloss colors)
- **`ThemeDefault`**, **`ThemeNord`**
- `ApplyTheme(t Theme)` — updates global styles, clears cache, triggers MarkAllDirty
- `ThemeNames() []string`, `LookupTheme(name string) *Theme`

---

## Package: `types`

### `types.go`
- **Constants**: `RoleSystem`, `RoleUser`, `RoleAssistant`, `RoleTool`
- **`Message`** struct: `Role`, `Content`, `Name`, `ToolCallID`, `ToolCalls []ToolCall`, `ReasoningContent`
- **`ToolCall`** struct: `ID`, `Name`, `Arguments string` (raw JSON)
- **`ToolDef`** struct: `Name`, `Description`, `Parameters map[string]any`
- **`ChatRequest`** struct: `Messages []Message`, `Tools []ToolDef`, `MaxTokens int`, `Model string`, `StreamCallbacks *StreamCallbacks`
- **`ChatResponse`** struct: `Content`, `ToolCalls []ToolCall`, `ReasoningContent`
- **`StreamCallbacks`** struct: `OnReasoningDelta`, `OnTextDelta`, `OnToolCall`, `OnToolResult`, `OnStepDone`
- **`Memory`** struct: `Key`, `Value`
- **`MemoryStore`** interface: `Remember`, `Recall`, `Forget`, `List`
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
5. **SSRF protection** — one shared policy in `internal/netsafe`: resolve once, validate every address, pin the validated IP in `DialContext`, re-validate each redirect hop, optional per-client loopback allowance; used by `web_extract`, the browser pre-flight/interceptor and the MCP HTTP transport
6. **7 fuzzy edit strategies** — exact → trimmed → ws → indent → escape → unicode → block-anchor
7. **5-level web extract fallback** — HTTP → Cloudflare → Google Cache → Wayback → Chromium, all behind a shared SSRF client that pins the validated IP in `DialContext` and re-validates every redirect hop; before Chromium starts, the observable HTTP redirect chain is walked with the same client (page-level JS/meta redirects remain a residual risk)
8. **Session persistence** — JSON on disk (mode 0600), fork/branch support, AI-generated titles; ids and fork labels are charset-validated and contained inside the session directory
9. **Context compression** — Hermes-style head/middle/tail at 50% of context window
10. **3-layer skill discovery** — builtin (embedded) → global → project, with override
11. **Incremental TUI rendering** — msgDirty/msgRowCount tracking, ~2.3ms constant render time
12. **Security sandbox** — command blocklist, symlink-resolved path containment, FIFO permission queue (`once`/`session`/`always`), and a `recover()` guard so a panicking tool cannot kill the process

## Known Limits (accepted residual risk)

- **Sandbox check-vs-open race**: file I/O uses the OS-resolved path returned by `CheckPathAccess`, and on Linux `CheckPath` additionally asks the kernel with `openat2(RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS)` whether the requested path really resolves inside the root (`tool/pathbeneath_linux.go`, inert on kernels < 5.6 and on other platforms). A symlink or magic link present at check time is therefore caught even when the resolved-string comparison was fooled. The I/O still happens on the resolved path rather than through the verified fd, so a swap between the check and the open remains theoretically possible.
- **bash process group**: a descendant that calls `setsid(2)` escapes the group kill performed on timeout.
- **Chromium fallback**: the rod path installs request interception (`Browser.HijackRequests`) and applies the SSRF policy to every request the browser makes — 3xx hops, JavaScript/`meta refresh` redirects, XHR/fetch, iframes and subresources; local-only schemes (`data:`, `blob:`, `about:`) are allowed since they never touch the network. Both browser paths also pin the top-level host to the address this process validated (`browserHostRule` → Chromium `--host-resolver-rules=MAP <host> <ip>`), so the initial navigation cannot be DNS-rebound; the pin falls back to the default launcher if the pinned one fails to start. Not covered: rebinding of redirect-target and subresource hostnames (their names only appear while the page loads, so they are checked at interception time but resolved again by Chromium), the `--dump-dom` exec path (`crawlViaExec`, `tryBrowser`) which cannot intercept requests at all and keeps only the pre-flight check plus the top-level pin, and browser-internal loads the Fetch domain may not pause (e.g. WebSocket upgrades, cached/service-worker responses).
- **MCP**: requests are concurrent (one reader goroutine with per-id dispatch) and a cancelled call only unregisters itself, leaving the transport usable; `tool.CloseMCPServers()` (called from `main.go`) closes every client and reaps stdio children on exit. Server-initiated requests are answered (`ping` and `roots/list` with a result, anything else with a `-32601` error) so a server is never left waiting, and `serverInfo`/`tools` are mutex-guarded. Remaining: the unmatched-message skip budget is shared by all pending calls rather than per-request.
- **`CheckCommand` and plan-mode checks** are advisory substring heuristics, not an OS-level boundary.

## Testing

- **501 test functions** across all packages (`go test ./... -count=1`)
- `go test -race ./...` passes; the race detector is enforced in CI (`make test-race`)
- Agent loop: 13 integration tests using `MockLLM` step-by-step
- LSP: 15+ tests with `io.Pipe`-based mock (no real LSP server needed) + single-reader correlation tests
- MCP: 22 tests
- TUI: CellGrid roundtrip, keyboard, mouse, streaming, selection, todo rendering
- Edit: 14 tests covering 7 fuzzy strategies
- Apply patch: 9 tests + sandbox gate tests
- Sandbox: symlink escape, permission queue, "allow once" semantics, process-group kill
- SSRF: redirect blocking, DNS pinning, non-public IP ranges (network-free)

## Build & Run

```bash
make build          # → bin/tinycode (CGO_ENABLED=0, stripped)
make test           # go test ./... -count=1
make test-race      # go test -race ./... -count=1
make test-repeat    # go test ./... -count=3 (catches leaked global state)
make lint           # go vet (+ staticcheck when installed)
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
- `cross` job: `GOOS/GOARCH` build + vet for linux/amd64, linux/arm64 and darwin/arm64 — this is also what type-checks the linux-only files (`tool/pathbeneath_linux.go`, `tool/sysproc_unix.go`).
- `staticcheck` job: advisory (`continue-on-error`) because staticcheck lags new Go releases; make it blocking once a release supports the CI toolchain.
- Toolchain drift: CI is on Go 1.27, the Nix flake pins 1.26 and `go.mod` declares 1.24.2. `gofmt` output differs between releases, so **the CI toolchain is authoritative for formatting**; align the others (or add a `toolchain` directive) when convenient.

## Dev Environment

- Nix flake (`flake.nix`) + `direnv` (`.envrc`) for reproducible toolchain
- Go 1.24.2, gopls, gofumpt pinned
- `nix develop` or `direnv allow` to activate
- CI: GitHub Actions (build+lint+test on push/PR, cross-compile+release on tags)
