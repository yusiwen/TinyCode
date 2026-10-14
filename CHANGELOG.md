# Changelog

TinyCode is developed as a sequence of small, verifiable changes. This file
records them: the work that is not yet tagged as a release first, then the
historical feature log that used to live at the end of `README.md`.

Entries keep the commit hashes they landed with, so a line here can be traced
back to its change and tests.

## Unreleased

### Model pricing: the decision not to ship a price table

[docs/pricing-decision.md](docs/pricing-decision.md) records what this repository will and will not do
about knowing what a call cost (issue #182). The verdict is **no vendored price table**: the route's own
reported charge (OpenRouter states `usage.cost` per request) and rates the user declares stay the only
two mechanisms, and a call neither can price is counted as unknown rather than as zero.

The measurement behind that is that the two leading open datasets disagree about this repository's own
default route. For `deepseek-flash` input, LiteLLM's base entry says $0.30/M and models.dev says
$0.15/M — and both are right, because LiteLLM carries DeepSeek's peak rate with an `off_peak_pricing`
block holding the other half, while models.dev carries the off-peak rate as its only number. A vendored
single value is therefore ambiguous rather than merely stale: which one is correct depends on the hour.
DeepSeek bills peak at 01:00–04:00 and 06:00–10:00 UTC, Monday to Friday, which is 20.8% of the week.

The same measurement exposes what has to happen instead. `agent.Price` has five lanes and no time
dimension, so the user's *own* declaration cannot be right either: `config.example.json` declares the
peak rates, which makes its estimate 2× too high for about 79% of the week with nothing saying so. That
schema work is [#225](https://github.com/yusiwen/TinyCode/issues/225), and the retired legacy model name
the default still sends is [#226](https://github.com/yusiwen/TinyCode/issues/226). `docs/roadmap.md` A3
now points at the verdict rather than at the open question.

No application code lands from #182; this document and those two issues are its deliverable.

### The tool registry lists the tools that exist

`CODEBASE.md`'s table of built-in tools is titled "Complete Tool Name Registry" and is written by hand.
It was not complete: `web_extract_browser` is registered in `main.go` and the table had no row for it —
an omission its row count hid, because the table has 24 rows once the dynamic `mcp_*` row is counted as
one of the tools, so the number a reader checks agreed with the documented "24 tools" while one real
tool was missing (issue #188). The table is complete again, and
`TestDocumentedToolRegistryMatchesTheCode` now reads both sides — the `Name: "…"` literals in `tool/`
plus the four `lsp` tool types, against that table's rows — so a tool the package gains without a row,
or a row without a tool behind it, fails the build.

The other half of the issue was a tool that never existed. `memory` was listed among the tools in the
README's System Overview and, long after #187's figure replaced that diagram, still sat in the TUI's
housekeeping whitelist as `{"todo": true, "memory": true}` — a name no registry has ever held. It is not
a tool: `agent/memory.go` implements `HTTPMemoryClient` against a remote service and nothing constructs
it, `Agent.Memory` is never assigned outside its own declaration, and recall is an agent-layer step
(`MemoryModeAuto`) that no caller enables. `CODEBASE.md` now says so where it describes the client and
the `MemoryStore` fields, the whitelist holds only `todo`, and `docs/roadmap.md` records the decision —
`memory` is a capability waiting for a caller (B1/B2, on a privacy decision), not a tool waiting to be
written.

Two stale line references in that roadmap entry were corrected in the same pass: `Agent.Memory`'s read
is at `agent/agent.go:264-265`, not 148-149, and the whitelist is at `tui/update.go:578`, not 536.

### Nested agent worktrees are ignored by git

An agent session keeps its scratch worktree inside the workspace root (`.worktrees/<scope>`) because
the session's file policy may allow writes only there. Nothing ignored those directories: after
`git worktree add .worktrees/probe …` the outer tree reported `?? .worktrees/`, and a plain
`git add -A` there staged the worktree as an *embedded repository* — a gitlink in place of the files,
which git warns about but records anyway in a commit that is easy to miss (issue #167).

`.worktrees/` is now in `.gitignore`, so a worktree created there is invisible to the outer tree's
status and staging, and `git worktree remove` is the cleanup path without a second step.

### The command palette and provider picker fit a narrow terminal

The input area's rows are drawn below the cell grid like the dialog's, so a row wider than the window is
cut by the terminal itself. The command palette's rows are `  <name>  <description>`; the widest is 36
cells, so a 30-column terminal showed `Toggle thinking display` as `Toggle thinking` and lost the rest.
The provider picker's labels are built from the user's own provider names —
`openrouter-anthropic-claude-sonnet (mock)` is 41 cells — so the picker could lose the end of the very
name the user is choosing (issue #221).

Both are now budgeted against the width. A description that fits beside its name in full stays there; one
that does not takes a line of its own under the row, where it gets the whole width, because splitting it
into the narrow column beside the name leaves fragments like `LSP` / `diagnostics`. A picker label is
wrapped like the dialog's heading, with the selected row's header style budgeted with it, and the row
marker stays on the first line so a wrapped label continues under its own text. The `Commands:` and
`Select provider:` headings are budgeted too — the second is 16 cells before its padding, wider than a
narrow terminal. A long token with no space in it is now split after a hyphen, slash or dot where one
fits (`openrouter-anthropic-` / `claude-sonnet`) and at the cell limit only when there is none
(`openrouter-anthropic-claud` / `e-sonnet`).

The suite gained a `provider` frame set (80x24 and 30x12) and a 30x12 palette, both with narrow
screenshots; no committed frame changes above 30 columns.

### The permission dialog fits a narrow terminal

The input area is drawn below the cell grid, so it is never trimmed against the terminal: a line wider
than the window is cut by the terminal itself, mid-word and without a mark. At 40 columns the permission
dialog's hint row was 54 cells — `↑↓ navigate · Enter select · 1-9 shortcut · Esc cancel` — and the user
read a sentence with no end, `Esc cancel` being the part that went missing; the heading was 47 cells, and
the always-allow answer names the config file it writes, so it is 60 cells before its `[3] ` marker is
added (issue #218).

The three hints are now lists of alternatives rather than finished strings, and the input area lays
itself out for the width it has: the heading and the answers wrap at word boundaries, an alternative is
never dropped to make room, and a wrapped hint line ends with its `·` so it still reads as one list — at
40 columns the shortcut row becomes two lines and `Esc cancel` survives. A word that cannot fit a line
even alone is split across lines rather than clipped, because the always-allow answer names the file a
permanent permission will be written to and a reader cannot consent to a path they can only half see. An
unknown terminal width still draws every line in full, as before.

The committed frames follow: `dialog_40x12` is regenerated, no committed frame is wider than the geometry
it claims any more, and `TestGoldenFrames` now asserts that per frame instead of requiring one frame to
overrun. The clip keeps an over-wide fixture of its own, since borrowing a screen that happens to
overflow would mean keeping a defect to test the clip.

### A long status bar keeps its status message

The status bar is one line: the session facts, then `│ <status message>`. The model rendered it in full
and the **terminal** clipped it at the window width, so as the segments grew — a budget fraction
(#179), a reported charge (#181) — the message at the end was what got cut. At 120 columns
`Press Ctrl+C again to quit` arrived as `Press Ctrl+C again`, and that phrase is the only signal that a
first Ctrl+C was taken as a quit rather than as an interrupt (issue #196).

`fitStatusBar` now budgets the line before it is rendered: the message is measured first and kept whole,
the remaining segments are added left to right while they fit, and a segment that does not fit is left
out **whole** — a segment cut in half can be misread (`0.95 credits` as `0.95 credit`, a fraction losing
its limit) where an absent one cannot. When no width can make the message fit, it is clipped with a
visible ellipsis rather than silently truncated. An unknown terminal width still builds the bar in full,
as before.

This also makes the committed frames honest for the status bar: they used to carry the same
126-character line at every geometry, because the *test* clipped and the model did not, so the goldens
pinned the text while the screen showed a cut sentence.

`status-cost.scenario` comes back from 200x50 to **120x30** — the width at which it used to fail — and
its exit sequence, which waits for the whole confirmation, is the assertion that the message survives.

### Resizing a running program is covered again

The TUI's in-process program-driver test was deleted when the harness moved into the `tuiprobe`
module, and it was the only thing in the repository that resized a *running* program: the scenario
runner had no `resize` step, so nothing drove a terminal resize into the real binary (issue #214).
`tui/geometry_test.go` still covered the model's answer to a `tea.WindowSizeMsg`, which made the
loss a narrowed property rather than an open hole — but the terminal's half was untested.

`tuiprobe v0.1.5` adds the step (the pty, session, daemon and CLI layers could all resize already;
only a scenario file could not reach them), and `tui/testdata/scenarios/resize-running.scenario` is
the guard: it opens the binary at 80x24, `mark`s the screen, resizes to 40x12, and then waits for the
input row with `--since` — text that can only match if the program redrew after the resize — followed
by `fit --size 40x12` and a screenshot whose pixel size is asserted against the new geometry. Deleting
the `resize` line makes the scenario fail, which is the control that keeps the assertion meaningful.

The `tuiprobe` pin moves to `v0.1.5`.

### A cold toolchain can fetch inside the boundary

`confine_commands` granted one root beyond the project — the platform user cache
directory — which is enough for `go build` and `go test` on a warm checkout and not
enough for a cold one: a module that is not already in the module cache could not be
downloaded by a confined command, because `GOMODCACHE` (`~/go/pkg/mod`) was not a
granted root and a confined command cannot create a directory it was never granted
(issue #166).

The granted set is now the user cache directory plus the download cache of every
toolchain installed on the host (Go's module cache, cargo's registry, npm's cache,
pnpm's store — each honouring its own environment variable). Two rules keep the
widening defensible, and both are tested:

- **A cache subtree only, never a directory on `PATH`.** `GOPATH` holds `go/bin`
  beside `go/pkg/mod` and `CARGO_HOME` holds `bin` beside `registry`; granting either
  home would let a confined command leave a program behind that the user later runs
  outside the boundary — the sandbox escaping by persistence. `PNPM_HOME` is refused
  for the same reason.
- **Only an installed toolchain, and only a cache that exists.** The second half is
  not pedantry: the Linux applier refuses to run with a root it cannot open, so
  granting a phantom path would break every confined command rather than widen the
  boundary. The composition therefore creates the installed toolchains' caches at
  startup, before it reads the roots — which is exactly what a cold checkout needs.

The set still lands through the one shared list (`WritableRoots`), so the file fence
and the command boundary cannot drift, and the drift guard stays green. `/sandbox`
now states the cache policy and the key that grants one more path, and
`docs/sandbox.md` names the whole set, both rules, and where to add a toolchain this
list does not know.

### A long generation is no longer cut, and the idle bound can fire

Two defects with one root cause: `agent/provider_openai.go` armed a single
`http.Client{Timeout: openAIRequestTimeout}` for both request shapes. Go's
`http.Client.Timeout` covers *reading the body*, so on a streaming call it was a total limit on the
generation — the opposite of what the file's own comment promises. Measured against a server delivering
a line every 30 ms: a healthy 300 ms answer was cut at 102 ms with `Client.Timeout or context
cancellation while reading body` (#185).

The same timer made the idle bound unreachable at the shipped defaults: both bounds are 120 s, and a
client timer starts at the request while the idle watchdog rebases on every line, so once a line had
arrived the client timer always won. The idle bound's diagnosis was therefore correct only in the tests,
which shrank the idle side and left the request side at 120 s (#186).

Both are fixed by taking the timer off the client. The streaming path's only bound is the idle
watchdog, which also covers the wait for response headers. The batch path — no interim progress to
watch — gets a watchdog on the same request context, naming its bound the way the Ollama provider does
(`no answer within 120s`). The transport keeps its own phase limits (dial, TLS) from the default
transport, and no bound is armed on the client itself, so neither defect can be reintroduced by
lowering a client timer.

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
**unknown**, and never folded into a total as zero — `Ollama` and subscription plans have no per-token
price at all, and "0" would read as free in exactly the decisions the number is there to inform. The
status bar shows money only once cost accounting is in play (a rate declared, or a charge reported),
and then shows the count of calls the total does not include beside it; a session with neither keeps
the bar it has always had. Whether this project should ship a price table of its own is a separate,
undecided question (#182).

### Transient provider failures are retried

A rate limit, a model still loading or a connection reset used to end the step. Both providers now
retry a failure that a second attempt could fix — 408, 429 and any 5xx, plus a transport error that
produced no response — with exponential backoff, equal jitter, and a floor at the `Retry-After` an
endpoint sends (its HTTP-date form is ignored rather than mis-parsed). The pause is capped, and the
call is bounded by the caller's own context, so Ctrl+C during a backoff stops the sequence instead of
being noticed after the next request.

The rule that keeps this honest is the **safe-replay** rule: a streaming attempt is repeated only when
nothing has reached the caller yet. Text already drawn cannot be drawn twice, and a duplicated answer
is worse than the failure, which at least says what happened. A batch call has nothing on screen, so it
is safe to repeat whole. A retry announces itself (`retrying (2/3): …` in the status bar, a line in
one-shot mode), because a pause with nothing on screen is indistinguishable from a hang, and a final
failure names how many attempts it took.

Not retried, deliberately: a context error, a body we cannot parse, and any other 4xx — a rejected key
or a missing model answers the same way however many times it is asked.

### A long task announces itself when it finishes

A background `task` used to end in silence — the user found out by asking `task_collect` or by noticing
the interface. A finished background task now announces itself, and so does a foreground run that
outlived a threshold.

- **terminal** (the default) writes `OSC 9` and `OSC 777`, which iTerm2, WezTerm, kitty, Ghostty and
  rxvt-unicode render as notifications; a terminal that knows neither ignores them, so this is safe to
  have on. **desktop** (opt-in: `channel: desktop` or `both`) runs the platform's notifier —
  `osascript` on macOS, `notify-send` on Linux — with the text passed as arguments rather than
  interpolated into a script, so a goal containing a quote cannot change what runs.
- `notify.min_duration_seconds` (default **30**) gates everything, so a two-second task stays quiet;
  `0` announces every completion. A threshold rather than a focus check, because there is no portable
  way to ask a terminal whether it has focus. `channel: off` turns the feature off.
- Nothing makes a sound, and there is no Windows notifier (`channel: desktop` is macOS/Linux). A
  machine without the binary is silent rather than noisy: the run it was announcing has already
  finished.
- The announcement text is sanitized: a title or body containing ESC, BEL or a line break has those
  bytes stripped, because that is how a task's own goal text could otherwise end the escape and write
  to the terminal.

Roadmap entry D5.

### A tool call is checked before it runs, and a loop is broken

A model's tool call went straight from "the JSON parsed" to the tool. A missing required field, a wrong
type or a value outside an enum reached `Execute` and came back as whatever that tool's own parsing
happened to say — or, worse, as a result that looked fine for a call that did something else.

Arguments are now validated against the same schema the model was shown, and a call that breaks it is
**not executed**: the model is told every problem with it at once (field, what the schema wants, what
arrived), so one step can fix three typos instead of three steps fixing one each. The validator
implements exactly the keywords the 24 built-in schemas use — `type`, `properties`, `required`, `enum`,
`items`, counted by a command rather than guessed — and ignores every other keyword, because refusing an
argument over a rule it cannot evaluate would be worse than not checking it.

A model that repeats the *same failing call* is now refused instead of being handed the same failure
until the step budget runs out: the call's identity is its tool and its arguments (key order does not
matter), it is counted per run, and after `repeatFailureLimit` failures the answer says so and says to
change the approach. A repeated **successful** call is never counted — polling a build is work, not a
loop — and a fresh prompt is a fresh start.

Roadmap entry F3.

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
