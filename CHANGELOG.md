# Changelog

TinyCode is developed as a sequence of small, verifiable changes. This file
records them: the work that is not yet tagged as a release first, then the
historical feature log that used to live at the end of `README.md`.

Entries keep the commit hashes they landed with, so a line here can be traced
back to its change and tests.

## Unreleased

### TUI verification moved to `tuiprobe`

The TUI visual harness's mechanism — terminal emulator, PTY scaffolding, golden
helpers, SGR→CSS renderer and Chromium plumbing, about 1,400 lines — now lives in the
nested `tuiprobe` module, released as `tuiprobe/v0.1.0` and pinned in `go.mod`.
`tui/` keeps the fixtures and the judgments: one scenario table (ten builders) plus
assertions that are a few lines each, in `tui/scenarios_test.go` and
`tui/verification_test.go`.

Proven before deleting: the 27 committed text goldens and the ANSI golden are
reproduced **byte for byte** through the same builders, and both suites ran green in the
same CI job. See [docs/tui-verification.md](docs/tui-verification.md) and
[tuiprobe/docs/parity.md](tuiprobe/docs/parity.md).

### Provider-reported token usage in the status bar

The status bar's `tokens:` counter now shows the number the provider reported, not
`len(text)/4` over the streamed answer. The request already asked every
OpenAI-compatible endpoint for usage (`stream_options: {include_usage: true}`), but the
usage-only chunk that answers it was thrown away twice over: the SSE parser skipped every
chunk without choices, and it stopped reading at `finish_reason`, which arrives *before*
that chunk. Ollama's `prompt_eval_count`/`eval_count` were dropped the same way.

The reported numbers now reach `Agent.UsageTotal` and the counter, which falls back to the
estimate for an endpoint that reports nothing. Because usage covers the prompt too,
`tokens:` is larger than it used to be — the estimate counted streamed output only. Roadmap
entry A2; cost and pricing accounting remain open with A3.

### A stream that goes quiet now fails instead of hanging

Reading to `data: [DONE]` is what made the usage chunk reachable, and it took away the early
return at `finish_reason` without putting anything in its place: an endpoint that sent a
complete answer and then held the connection open left the step waiting for the provider's
whole 120 s request timeout, and the text it had was handed back as a **successful** answer.
The OpenAI-compatible provider now carries the same pair of bounds the Ollama one does — a
whole-request bound for a batch call, and a per-line idle bound (2 min) for a stream — and a
read that is cut returns an error naming the bound instead of a partial answer. A caller
cancellation is still recognisable as one, so an interrupted run keeps reading as
interrupted. Reported as #176.

### A run can be stopped at a token budget

`budget.max_tokens_per_run` and `budget.max_tokens_per_session` bound what one prompt's ReAct loop and
a whole session may spend, counted in the tokens the provider reports. Both default to `0` =
unlimited, so a configuration that does not set them behaves exactly as before.

The loop checks before every provider call — the only point that spends — and a run that reaches
either limit ends with a message naming the limit, the spend and the knob to raise, instead of making
one call too many. A provider that reports no usage is counted as an estimate of its output, so a
silent endpoint cannot switch the budget off; such a call never fires the usage event, which stays a
report of what the provider actually said. Sub-agents enforce the same limits against their own
spend — attributing a sub-agent's tokens to its parent is roadmap G1. Roadmap entry A3.

### Cost accounting: what a call charged, and what it cost

Two things were missing to answer "what did this session cost", and they are separate problems.

**What a route charges.** Some routes report it per request: OpenRouter returns a `cost` in its usage
object, and a `prompt_tokens_details` split of cached and cache-written input beside it. Those numbers
are now carried through unchanged — never recomputed, never added to a figure derived from a price
list — with `cost_currency` on the provider naming the unit that route bills in.

**What the tokens were.** A route that does not report a charge has to be priced from rates, and rates
need the lanes: DeepSeek bills four input lanes (cache hit vs cache miss) times a peak/off-peak rule,
which a single input price cannot express. `Usage` now carries `cached_prompt_tokens`,
`cache_write_tokens` and `reasoning_tokens` — one field per billing lane, so OpenAI's nested
`prompt_tokens_details` and DeepSeek's flat `prompt_cache_hit_tokens` land in the same place.

`pricing` in the configuration then declares rates per million tokens, keyed by `<route>/<model>` with
`*` wildcards, because a price belongs to a billing endpoint rather than to a model name. A call is
priced from a declaration only when its tokens were *reported*: multiplying a declared rate by a
guessed token count would dress a second estimate as a measurement. Everything else is counted as
**unknown**, which the status bar shows as an unknown rather than as zero — `Ollama` and subscription
plans have no per-token price at all, and "0" would read as free in exactly the decisions the number
is there to inform. Whether this project should ship a price table of its own is a separate,
undecided question (#182).

## v0.0.7 — 2026-10-01

### Language server integration

- LSP documents are synced with their real text (`didOpen` with content, then
  `didChange`) instead of being registered as empty buffers, and `Init` rebinds
  the server when the workspace root changes, so diagnostics are no longer
  "not included in your workspace" noise. (`d2cf66e`)
- The server is chosen from the project language, falling back to the touched
  file's own extension; a file with no configured server now fails fast instead
  of being handed to `gopls`.
- The LSP integration tests run in CI against a pinned `gopls` (0.23.0) through
  `make test-lsp`, and the mock server answers navigation requests so the
  go-to-definition, references, hover and document-symbol paths are covered.

### Security and sandbox

- Linux path containment is checked by the kernel (`openat2` with
  `RESOLVE_BENEATH`), including through symlinks, without rejecting in-root
  symlinks. (`e947654`, `da2f077`)
- The SSRF policy is shared with the MCP HTTP transport, the browser's
  top-level host is pinned to the validated IP, and Chromium requests are
  intercepted so a redirect cannot bypass the check. (`6031cf5`, `5122376`,
  `dfbbe16`, `7f3db7b`)
- The loopback exemption is scoped to the configured authority. (`3cc21f4`)
- A sandbox denial is refused immediately when no permission dialog exists,
  instead of waiting for a prompt that can never appear. (`ddf483e`)

### Reliability

- MCP requests are correlated by id with per-call cancellation, and each
  request now carries its own skip budget: one starved call fails on its own
  instead of aborting every call in flight. (`c97447b`)
- Server-initiated MCP requests are answered so the peer is never left waiting,
  and client metadata is locked. (`97aba4b`)

### Tooling

- CI additionally enforces `gofmt`, the race detector, repeated runs
  (`-count=3`), a cross-compile/vet matrix and a blocking, pinned
  `staticcheck`. (`0bf8a1c`, `abe33ed`, `5b8df7d`, `b50ec21`)
- Generated and exported output is written with private permissions. (`78f4d9e`)

### Browser sandbox

- The headless browser now runs behind a loopback filtering proxy
  (`internal/browserproxy`). Chromium resolves DNS, follows redirects and loads
  subresources itself, so the previous pre-flight check and the rod request
  interceptor could not cover the `--dump-dom` exec path, and a redirect or
  subresource hostname was checked by name but resolved again by Chromium. Every
  connection now asks the proxy, which resolves each hostname once, validates
  every address against the shared SSRF policy and dials only a validated,
  pinned address; private, loopback and metadata targets are refused with 403.
  Redirects are handed back to the browser (so the next hop is validated too) and
  QUIC is disabled because HTTP/3 would leave over UDP without asking the proxy.
  Loopback does not bypass the proxy (`--proxy-bypass-list=<-loopback>`), and the
  rod path keeps the request interceptor and the top-level host pin as fallbacks.
- Policy refusals from `internal/netsafe` now wrap a sentinel (`ErrBlocked`) so
  a caller can answer 403 for a blocked target and 502 for an unreachable one.
- `bash` timeout now reaps a descendant that escaped into its own session with
  `setsid(2)`: the process tree is collected and killed before the process group,
  using the kernel's process table on darwin and `/proc` on linux (no `ps`
  subprocess). A double-forked descendant that re-parents to init before the walk
  remains out of reach, as documented.

### Browser detection and smoke test

- `findPlaywrightBrowser` now recognises the layouts Playwright actually ships.
  It only knew `chromium-<rev>/chrome-mac/Chromium.app`, so a current
  installation (Chrome for Testing under `chrome-mac-arm64`, or a
  `chrome_headless_shell` package) was invisible and the browser tool silently
  downloaded its own copy instead. Revisions are compared numerically (999 does
  not outrank 1000), the full browser is preferred over the headless shell, and
  the rod path hands the detected binary to its launcher.
- `make test-browser` (`BROWSER_TEST=1`) drives both browser paths against a
  loopback page that only shows its text after JavaScript runs, and asserts the
  request reached the server through the filtering proxy (the proxy's `Via`
  header). A `browser` CI job runs it with Chrome for Testing; the test skips
  without a browser, so ordinary test runs never launch one.

### Sandbox

- File I/O in `read_file`, `write_file`, `edit` and `apply_patch` now opens
  through the sandbox root with `openat2(RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS)`
  on Linux, so the kernel decides containment on the same descriptor that is read
  or written and the documented check-then-open window is closed for in-root
  paths. Paths allowed outside the root, other platforms and kernels older than
  5.6 keep the previous plain-open behaviour, so the layer only ever adds
  enforcement.

### Testing and verification

- Fuzz targets now cover the fuzzy edit matching (a match must be deterministic
  and its range must hold the text it claims), indentation correction, the
  Levenshtein metric, the sandbox `relBeneath` helper, the TUI's word wrapping
  and markdown parser, the MCP `Content-Length` frame reader and the SSRF IP
  policy. `make fuzz` runs all of them (`FUZZTIME=30s` by default); their seed
  corpora run with every `make test`.
- Fuzzing found and fixed a silent file-corruption bug: a search holding a lone
  UTF-8 lead byte matched the first byte of a multi-byte character, so the edit
  replaced one third of it. It also found that `normalizeAuthority` was not
  idempotent for authorities with an unmatched `]`, and that
  `agent.ToolAllowedFor` panicked on a nil config.
- Coverage at the release: types 100%, skill 91.8%, tlog 91.7%, agent 90.2%,
  browserproxy 90.1%, session 88.9%, mcp 84.0%, tui 81.6%, root 81.0%, config 80.9%,
  tool 79.3%, netsafe 78.7%, lsp 74.2% (710 test functions, 10 fuzz targets, measured
  with `GOPROXY=off go test -count=1 -cover ./...`). The README badge carries the test
  count and `CODEBASE.md` lists every count together with the command that produces
  it, badge included.

### Release and tooling

- `release.yml` moved to Go 1.27: the `go.mod` directive now requires it, so the
  previous 1.24 pin would have failed the release pipeline.

### Bounded work and deadlines (2026-10-01)

- The Ollama provider no longer trusts a stalled endpoint: a request deadline plus an
  idle watchdog that resets per streamed line, so a server that stops answering
  mid-stream fails with a named error instead of hanging the run. (`fd45f67`, #1)
- `tools/call` gets a default deadline when its caller supplied none, and a response
  that arrives just as the stream closes is delivered rather than reported as
  "reader stopped". (`d632a6a`, `617b5b2`, #2, #39)
- A browser probe that ran out of its budget is no longer cached as permanently
  unusable; the verdict expires after 30 s and is retried. (`a2adc87`, #26)

### File and process safety

- `apply_patch` checks the read its apply pass depends on, so a stale or unreadable
  target fails the patch instead of truncating the file. (`bbd1b37`, #6)
- `writeSandboxed` replaces files atomically: the bytes go into a temp file in the
  target's own directory through the sandbox-aware open, one rename moves it over the
  target, the existing mode is preserved and the temp is removed on every failure
  path. An interrupted write can no longer leave an empty or half-written file.
  (`e9a2935`, #36)
- Where `openat2` cannot reach — a path allowed outside the root, macOS, kernels older
  than 5.6 — the resolved path is walked one component at a time with `O_NOFOLLOW` and
  re-resolved before the open, so a component swapped for a symlink is refused
  (`ELOOP`, `errPathChanged`) instead of followed. (`dd5bd27`, #7 S1)
- The language-server tools hand the server the bytes they actually read instead of
  re-opening the path, closing the window between the sandbox check and the server's
  own read. (`3fa7a1d`, #7 S2)
- On Linux, bash commands run inside a cgroup: a descendant that double-forks and
  escapes the process group is killed with the cgroup when the command times out, so
  it cannot outlive the tool call. A portable test proves the escape still happens
  without the cgroup, which is what keeps the Linux-only path justified. (`9294296`,
  #9)

### Browser sandbox

- The headless browser disables QUIC, WebRTC's non-proxied UDP and background
  networking, and refuses to start without the filtering proxy instead of falling back
  to a flags-less launch. (`9b4462f`, #8)
- The `--dump-dom` path prefers the Playwright headless shell: on macOS the full
  desktop build produced no DOM at all (nothing in 120 s) while the shell from the
  same revision answered in about a second. The rod path keeps the full browser and an
  explicit `CHROME_PATH` still wins over both. (`a5f7bfc`, #43)

### Coverage, tests and the second language server

- The search fallbacks (`rg` → `grep` → Go walk), the HTTP MCP transport and the
  filtering proxy are covered, and the proxy gained a fuzz target asserting that a
  refused target never reaches the dialer. The search ladder now falls through to the
  portable implementation when an installed tool fails, instead of failing the whole
  search. (`f367f37`, #4)
- A second language server is exercised end to end, which immediately found a real
  bug: the client advertised no capabilities at all, and tsserver gates every
  diagnostic on `textDocument.publishDiagnostics`, so every TypeScript file looked
  clean while gopls pushes regardless. (`7a9e587`, #10)
- The dead `no_ollama` build tag was removed; it broke `go build -tags no_ollama`.
  (`54eeb1f`, #5)

### Documentation and discipline

- The TUI visual harness has a reference (`docs/tui-visual-harness.md`), the harness
  and verification rules it produced are recorded in `AGENTS.md`/`CODEBASE.md`, and
  the gated checks document how to run them locally. (`353872f`, `66348df`, `ebb5d60`,
  #11)
- The test-count badge is back inside the measurement discipline: the README badge
  said 564 while `CODEBASE.md` said 667, both wrong. Every count now names the command
  that produces it, the badge included. (`ebb5d60`, #32)


## Historical feature log

The list below is the project's feature log up to the point where it moved out
of `README.md`. All planned features of that phase are implemented; the MCP
client supersedes the original plugin-system proposal.

- [x] **Incremental CellGrid Rendering** — msgDirty tracking, View() skips unchanged messages. Benchmark: ~2.3ms regardless of session size. (ffbbf22)
- [x] **Agent-level unit testing framework** — 13 integration tests using MockLLM step-by-step. (a3868e2)
- [x] **Multi-agent session tree** — `/fork` + `/session` branching conversations. (93f1665)
- [x] **Theming** — default + nord palettes, `/theme` command, persists to config.json. (e4bcf85, 533d167)
- [x] **Session management** — delete, export Markdown, search via CLI flags. (2236e84)
- [x] **LSP** — long-lived connection, background diagnostics, mock test framework, incremental diagnostics (SnapshotBaseline+GetNewDiagnostics), TUI error tracking (LSPDiagMsg, status bar "errors: N", /diagnostics command). (2ab4338, ace09ff, a2e3e07, 290818a)
- [x] **GitHub Actions CI/CD + Makefile improvements** — main.yml (build+lint+test), release.yml (cross-compile+release), Makefile test/releases targets. (ab07697, bddeed5)
- [x] **Skills & Subagents** — SKILL.md-based discovery + /skill command + 2 builtin skills. 3 new subagents: general (parallel research), compact (history compression), title (session naming). /explore command removed (explore kept as subagent). (cbd6db3, 8fa8800, adfa51b, c0b8ae8)
- [x] **Todo Feature — P0+P1+P2 Complete** — TodoStore + todo tool + JSON Schema (P0), TUI rendering with [x][>][ ][~] markers (P1), compression protection + housekeeping mute + session recovery (P2). 21 new tests. (2f51d06, 94db0e3, 25caefc)
- [x] **Todo TUI display fixes** — Always render TODO between reasoning and tool calls (not gated by todoDirty). Hide `todo` from tool call list. Persist across CellGrid rebuilds. Add blank line separator between TODO and `→ Calling tools:`. Render-acknowledge gate for concurrency safety. (720c8b6, 12ae5db, 01410a9)
- [x] **Line-Level Code Edit — edit + apply_patch** — Search/replace edit tool with 7 fuzzy strategies + indentation correction. V4A multi-file patch format. 23 new tests. (d067156, 9045176, 34d2c17)
- [x] **Title Agent & Session Titles** — title hidden agent generates conversation titles via LLM after first exchange. Applied on session save. (2deee5e)
- [x] **Edit Fuzzy Matching** — 7 fallback strategies (line-trimmed, ws-normalized, indent-flexible, escape-normalized, unicode-normalized, block-anchor). Indentation correction. 6 new tests. (34d2c17)
- [x] **Web Tools Phase 1-3** — web_search (DuckDuckGo Lite + SearXNG), web_extract (HTTP→CF→Cache→Wayback→Chromium, SSRF, LLM summary). 26 new tests. 21 tools total. (5c90f86, 4cb7ce1, 4bf27e5)
- [x] **SearXNG Config** — `searxng_url` field in config.json, wired via SetSearXNG(). (405bc87)
- [x] **MCP Client (4 Phases)** — stdio/HTTP transports, auto-discovery, agent.Tool registration, resources, SSRF security. 22 tests. 359 total. (e31c08b, cc1ba8e, 7b4fe75)
- [x] **Permissions engine (Ruleset)** — Replaced DeniedTools/AllowedTools with `{action, resource, effect}` Ruleset. Last-match-wins evaluation. Whitelist mode (`*: deny` + specific allows). FilterTools for sub-agent creation. (378a0fd)
- [x] **Async task + task_collect** — Background task manager for parallel sub-agent execution. `task({..., bg:true})` returns task_id immediately. `task_collect({id})` waits for completion. (dd1688f)
- [x] **Concurrent tool execution** — Multiple tool calls in the same step run concurrently via goroutines + result channel. Agent waits for all to complete before next step. (e5fd457)
- [x] **Sub-agent sandbox propagation** — `PermissionRequest.AgentLabel` tracks which agent requested path access. TUI dialog shows `[general] Write to /path?`. `SetAgentLabel()` called before sub-agent creation. (d0653ee)
- [x] **Prompt improvements** — Build mode system prompt guides LLM to use `task()` for parallel delegation, use relative paths, and separate paragraphs with blank lines. Explore agent has dedicated PROMPT_EXPLORE-style prompt. (effbe2c, a02d894, f66f151)
- [x] **Command palette UX fix** — Selecting a command from palette fills the input box. User presses Enter again to execute. No more immediate execution + stale text. (118e938)
- [x] **Status bar processing indicator** — Animated dot spinner + ● fallback indicator during streaming. Shows when agent is actively processing. (6adfda0)
- [x] **Mouse selection clamp** — `posFromCoord` clamps to last valid row instead of returning `Offset: -1` when dragging past content end. 5 new tests. (39e80e5)
- [x] **Multi-message step split** — Each agent step creates its own assistant message with independent reasoning, tool calls, TODO snapshot. `OnStepDone` callback fires between steps. (c5f854f, 4ef98ff)
- [x] **TODO snapshot per message** — Each assistant message carries the TODO list state at creation time. Resume restores TODO from history. Todo render ack fixed — ackCh moved outside TODO injection block to prevent agent deadlock. (e34143c, 633a641)
- [x] **Remove redundant UI** — `⏳ Running...` indicator removed (spinner in status bar is sufficient). `(processing...)` text in input area removed. `Response:` label removed (message color differentiates roles). (4e832f1, b35bdd7)
- [x] **[Copy] button fixes** — Button row counted in `msgRowCount` so next message doesn't overlap. `activeButtons` preserved per `MsgIdx` across renders so button stays clickable after incremental rebuild. (f9143cc, 8dffd64)
- [x] **Spinner pipeline fix** — Tick kept alive even during idle. `StatusStreaming` preserved across intermediate steps so spinner doesn't stop mid-task. Switched to braille `Dot` type (user terminal supports it). (9a0f55b, afc14e1, 42b8794)
- [x] **Tab mode switch** — Tab now always switches plan/build mode (removed empty-input guard). (c75d9ae)
- [x] **Ctrl+C copy fix** — Character-level copy requires non-zero selection range (click without drag no longer copies single char). Ctrl+C twice now quits. (ea6711c)
- [x] **Permission dialog** — 4 options (Allow once=true once, Allow session=session persisted, Always allow=config persisted, Deny). Dialog auto-shows on View() without keypress. Dialog dismissed after Deny (Resolved field). Alignment: `>` separate column, `[1]` `[2]` aligned. (3e6f306, 57359db, 792120f, e8cff4f, de01f54)
- [x] **read_file triggers dialog** — Previously returned text hint to LLM; now calls `RequestPermission()` like `write_file` does. (e2832e3)
- [x] **Session resume restores history** — `inputHistory` populated from loaded user messages. `historyPos` reset to -1 so Up goes to most recent entry. (444dd04, 2e6f9b3)
- [x] **Duplicate tool call fix** — `OnToolCall` fired twice per tool (main goroutine + execution goroutine). Removed duplicate from execution goroutine. (cc5a224)
