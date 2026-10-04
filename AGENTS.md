# TinyCode — AI coding agent in Go

## Language Policy
All code comments and documentation must be written in **English**, unless explicitly asked to use Chinese.

## Quick Start

```bash
make build          # build to bin/tinycode
make run PROMPT="..."  # build + run in one-shot mode
make test           # run all tests
make lint           # go vet + staticcheck
make test-tui-visual   # TUI frame screenshots + PTY smoke (TUI_SHOT=1); needs Chromium
```

When a TUI change has to be *seen* rather than asserted on: `make test-tui-visual`
renders the committed frame scenarios to PNGs in `TUI_SHOT_DIR` (default `/tmp`) and
starts `bin/tinycode` on a real 80x24 PTY. Update the frames with
`go test ./tui -run Golden -update` after an intended layout change, and read the
regenerated PNG to check the result.

Run interactively:
```bash
./bin/tinycode                          # TUI mode
./bin/tinycode "refactor the parser"    # one-shot mode
./bin/tinycode --list-sessions          # list saved sessions
./bin/tinycode --resume=TUI-20260607-235959  # resume session
```

## Local Verification

Inside the agent sandbox the suite needs two overrides: `HOME` (two `skill` tests
write under `~/.tinycode`) and `GIT_CONFIG_GLOBAL` (the host sets
`commit.gpgsign=true`, which fails the `tool` git tests). Redirect the Go caches
too, since `~/go/pkg/mod` is not writable there.

```bash
export GOPATH=/tmp/dsh-go GOMODCACHE=/tmp/dsh-go/pkg/mod GOCACHE=/tmp/dsh-go/build
export HOME=/tmp/dsh-home GIT_CONFIG_GLOBAL=/tmp/dsh-empty-gitconfig
go test ./... -count=1
go test ./... -count=1 -race
```

Every gated target skips itself without its env var, so a plain `make test` never
launches a browser or needs a terminal device. Run them deliberately:

```bash
make test-tui-visual     # TUI_SHOT=1: a real PTY (/dev/ptmx) and a fresh bin/tinycode
make test-tui-scenarios  # TUI_SHOT=1: same, for the scenario files (black-box)
make test-browser      # BROWSER_TEST=1: needs a Chromium
make test-lsp          # LSP_TEST=1: needs gopls on PATH
```

`tool.FindBrowser` discovers Chromium through `CHROME_PATH`, `CHROME`, `PATH` or
the Playwright cache under `$HOME`. If `HOME` is redirected (as above), set
`CHROME_PATH` or the visual harness skips with "no Chromium/Chrome installed".

## Test Harness Rules

TUI verification runs through **[tuiprobe](tuiprobe/README.md)** — a nested module in
this repository, pinned in `go.mod` (the pin is the only place its version appears). It owns the
mechanism: the terminal emulator, golden files with `-update` and diffing, the PNG
renderer (pure Go by default, Chromium optional), the PTY session with named and
bounded stages, and the browser discovery. `tui/` keeps the fixtures and the
judgments: which screens matter, at which geometries, and what they should look like.

- `tui/scenarios_test.go` — one scenario table and the ten builders. Adding a screen is
  an entry here plus an intended layout change.
- `tui/verification_test.go` — the assertions, each a few lines: `golden.Assert` for
  frames (byte for byte, `-update` aware), `golden.Assert` for the ANSI frame,
  `golden.Deterministic` for stability, `golden.Clip`/`FitsWidth` for geometry,
  `tuiprobe/render/font` for images, `tuiprobe/session` for the real binary on a PTY.
- `tui/testhelpers_test.go` — the few helpers that belong to other tests (a stream
  waiter, `frameDiff` as a wrapper over `golden.Diff`, the TrueColor profile pin).

The rules those helpers used to encode are now the tool's design, and they still
apply to every test that drives an external process: bound and name every stage
(`session.Stage`), return errors instead of panicking inside one, kill before waiting
and bound the wait, take a fresh deadline per operation, make timing knobs injectable,
and assert a visual artifact's dimensions against the geometry it claims.

The tool's own reference is [tuiprobe/README.md](tuiprobe/README.md) and
[docs/parity.md](tuiprobe/docs/parity.md) (what replaced what, capability by
capability); this repository's side is [docs/tui-verification.md](docs/tui-verification.md).

### Adding a TUI scenario

Three routes, in the order to try them.

**1. A frame in the suite — the default.** Add a builder in `tui/scenarios_test.go`
and one entry to `frameScenarios`: `sizes` for the goldens, and a `shotSpec` if the
screen also deserves an image.

```bash
go test ./tui -run Golden -update                 # commit the new expectation
make build && TUI_SHOT=1 go test ./tui -count=1   # images land in TUI_SHOT_DIR
```

Read the regenerated PNG before committing. The golden proves the text did not
change; the image is the only thing that shows it is *right* — looking at one is how
the overlapping-glyph bug in the Chromium renderer was found ("TinyCode TUI" drawn as
"TnCd U").

**2. A black-box scenario — interaction, or the real binary.** When the property is
about `main.go` wiring, flags, an interactive sequence or a live stream rather than a
single frame, write a scenario file under `tui/testdata/scenarios/*.scenario` and run
it with the pinned tool:

```bash
make test-tui-scenarios                       # every file in tui/testdata/scenarios/
go run github.com/yusiwen/TinyCode/tuiprobe/cmd/tuiprobe run \
  --gate TUI_SHOT --dir . tui/testdata/scenarios/startup.scenario
```

The `go run` line takes the version from `go.mod` (no `@version`): one place to bump.
There are three files today: `startup` (80x24, geometry, exit code), `narrow` (40x12)
and `palette` (`/` opens the command palette, typing filters it, Esc closes it — note
that Ctrl+C while the palette is open *closes the palette*, so Esc comes first).

Steps are `open`, `send`, `wait`, `stable`, `sleep`, `golden`, `diff`, `fit`,
`screenshot`, `close`, `wait-exit`, `expect-exit`; [tuiprobe/docs/scenario.md](tuiprobe/docs/scenario.md)
is the reference. The runner is a CLI feature (its package is internal), so scenario
files run through `make test-tui-scenarios` — which is also the CI step in the
`tui-visual` job — and never through a Go test. `wait-exit` before `expect-exit` when
the program leaves on its own: checking its code too early kills it and reports `-1`.

A fourth file, `live-answer`, is the one that spends money: it makes a real provider
call through this machine's `~/.tinycode/.env`. Run it deliberately with
`make test-tui-live` (gated by `TINYCODE_LIVE`; CI never runs it), and read the error
before blaming the key — `dial tcp`/`TLS handshake timeout` is the network, `401` is
the key.

`make test-tui-scenarios` wipes `/tmp/tinyscen-home` before it seeds the session fixture —
scenarios share that home, so a leftover from an earlier run changes the world a later
one sees (issue #96). A **manual** `tuiprobe run` does not wipe anything: if a scenario
behaves differently by hand than in the suite, reset that directory first.

A step or string in a scenario must come from the flow that scenario drives — four assertions in this suite were wrong that way (a regex character class, a repeated SGR parameter, a line borrowed from another loop, Ctrl+C sent to an exited one-shot run). When it does not fit, delete it; do not adjust it until it passes.

**3. A tool change.** If the missing capability is generic — another step, a renderer,
a better diff — extend `tuiprobe/` instead of growing a helper here, cut a
`tuiprobe/v*` release, then bump the pin in `go.mod`. The module boundary is what keeps
this repository free of harness code: a new local emulator, PTY wrapper or golden
helper is the signal to take this route.

## CI Notes

`main.yml` jobs: `ci` (build, gofmt gate, vet, tests, `-race`, repeated run),
`lsp`, `browser`, `tui-visual`, `cross` (linux/amd64, linux/arm64, darwin/arm64)
and `staticcheck`. The annotation baseline is **not zero**: one `ubuntu-latest`
migration notice per job, and `browser` + `tui-visual` additionally carry the
`setup-chrome@v1` Node 20 deprecation warning. Gate on *no new* annotations:

```bash
gh api "repos/$(gh repo view --json nameWithOwner -q .nameWithOwner)/commits/$SHA/check-runs?per_page=50" \
  --jq '.check_runs[] | "\(.name): \(.conclusion) annotations=\(.output.annotations_count)"'
```

## Project Structure

```
agent/          ReAct loop, LLM providers, context compression, registry
config/         JSON config loading (default → ~/.tinycode/ → ./.tinycode/)
lsp/            LSP client (gopls, pyright, etc.), diagnostics, 4 tools
mcp/            MCP client (stdio/HTTP), JSON-RPC 2.0, auto-discovery
session/        Session persistence (JSON), fork/branch support, export
skill/          SKILL.md discovery (3 layers: builtin/global/project)
tlog/           Structured logger (file + level filtering)
tool/           24 tool definitions (bash, edit, git, web, LSP, task, MCP, ...)
tui/            Bubble Tea TUI (CellGrid, markdown render, cmd palette)
types/          Shared types (Message, ToolCall, ChatRequest, StreamCallbacks)
main.go         CLI entry (cobra), dependency wiring
```

## Architecture Overview

```
User Input → TUI / CLI
  → Agent.Run() (ReAct loop)
    → LLM Provider (streaming SSE, reasoning + text deltas)
    → Tool execution (concurrent goroutines, permissions checked per-call)
    → No tool calls → final answer
  → Stream callbacks → TUI incremental render (CellGrid)
```

### Agent Loop (`agent/agent.go`)
- `Run(ctx, prompt)` — core ReAct loop with step budgeting
- Compresses history at 50% of context window (head + LLM-summarized middle + tail)
- Supports multi-turn history, tool call parallel execution, security block detection
- 6 named agents managed by `Registry` (plan/build/explore/general/compact/title)

### Providers (`agent/provider*.go`)
- `LLMProvider` interface: `Chat(ctx, ChatRequest) → ChatResponse`
- OpenAI-compatible (`agent/provider_openai.go`) — streaming SSE, tool calls
- Ollama (`agent/provider_ollama.go`) — local LLMs
- MockLLM (`agent/mock_llm.go`) — scripted responses for testing
- `ProviderRegistry` — runtime switch via Tab key

### Tools (`tool/`)
Each tool is `{Name, Description, Parameters (JSON Schema), Execute(ctx, args)}`.
24 built-in tools across categories: shell, file r/w, search, edit, git, web, LSP, task, todo, skill, sandbox.

### TUI (`tui/`)
- Bubble Tea framework with custom CellGrid frame buffer
- Incremental markdown rendering (~2.3ms), reasoning folding, char-level selection
- Status bar, command palette, permission dialog, todo display
- Theme system (default/nord)

## Code Conventions

- **Language**: Go 1.27, no external code generators
- **Imports**: stdlib first, then third-party, then internal (grouped by blank lines)
- **Error handling**: return `fmt.Errorf("context: %w", err)` with lowercase message
- **Testing**: `_test.go` alongside source, `MockLLM` for agent loop tests
- **Comments**: English only unless user explicitly requests Chinese
- **Configuration**: struct tags `json:"field_name,omitempty"` with snake_case

## Dependencies

- `bubbletea` / `bubbles` — TUI framework
- `lipgloss` — ANSI styling
- `go-openai` — LLM provider
- `goldmark` — markdown parsing
- `cobra` — CLI
- `go-rod` — headless Chromium (web_extract fallback)
- `rod` — headless browser for web extraction

## Key Design Decisions

1. **No external monocle/renderer** — custom CellGrid frame buffer renders markdown directly
2. **Concurrent tool execution** — multiple tool calls per step run in goroutines
3. **Permissions engine** — Ruleset with last-match-wins, wildcard `*`/`?` support
4. **MCP first-class** — native stdio/HTTP MCP client, no SDK dependency
5. **SSRF protection** — DNS resolution + IP blacklist for HTTP transports
6. **7 fuzzy edit strategies** — exact → trimmed → ws-normalized → indent → escape → unicode → block-anchor
7. **5-level web extract fallback** — HTTP → Cloudflare → Google Cache → Wayback → Chromium
