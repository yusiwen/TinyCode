# Roadmap

Every milestone is independently verifiable; nothing is "done" until its
acceptance line is met and recorded in a pull request.

## M0a — module, emulator, golden engine, replay command ✅

- Nested module `github.com/yusiwen/TinyCode/tuiprobe`, minimal dependencies
  (`mattn/go-runewidth` only).
- `screen`: the terminal emulator (moved from TinyCode's harness) plus `Render()`
  for ANSI output, with its nine tests.
- `golden`: golden files with `-update`, normalization and first-difference diffing.
- `tuiprobe replay`: a captured stream → text / ANSI / HTML.
- CI: the nested module is tested, vetted, linted and cross-compiled explicitly
  (the root's `./...` cannot see it), with its own prefixed-tag release workflow.

**Acceptance**: `cd tuiprobe && go test ./... -race` green; `go vet ./...` clean;
the module builds for linux/amd64, linux/arm64 and darwin/arm64; `make test-tuiprobe`
wired into the `ci` job and observed green there.

## M0b — the PTY session engine ✅

- `pty`: spawn any command on a PTY with a size applied *before* the child runs
  (`StartWithSize`), resize it with SIGWINCH, read the stream (EIO on exit is EOF),
  and reap it — kill the process group first, then wait, bounded by `CloseGrace`.
  A zero or negative geometry becomes 80x24, never a 0x0 terminal.
- `session`: the engine that mirrors the stream into a screen and keeps the raw
  trace; `Send` with a key vocabulary (`enter`, `ctrl+c`, `alt+left`, `f5`, …),
  `Text`/`ANSI`/`HTML`, `Resize`, `WaitText`, `WaitStable`, `Trace`, `Close`.
- Tests drive a fake TUI re-executed from the test binary: it paints a styled
  frame, reads raw keystrokes, animates, reports its geometry and exits with a code.

**Acceptance**: `go test ./session ./pty -race` green — including the size floor,
the bounded close, the key-byte assertions and the wait timeouts that name the step.

## M0c — sessions a CLI can share

- `pty`: spawn any command on a PTY with a size and an environment, resize it, read
  the stream, and reap it (kill before wait, bounded).
- A daemon with a unix socket that owns the sessions, so separate CLI invocations
  (`open`, `send`, `wait`, `text`, `ansi`, `close`, `sessions`) act on one program;
  idle exit; `sessions` to list.
- `--json` on every command: size, cursor, alt-screen, exit code, artifact paths.

**Acceptance**: drive a TUI binary through five separate CLI invocations, get the
text screen and the exit code, and leave no orphan process behind (checked with a
process-table assertion, not a `ps` sample).

## M1 — assertions

- `wait --text <re>` and `wait --stable <dur>` (bounded, with the stage named in
  the failure), `ansi`, `trace` (raw byte log), `diff --against <golden>`.
- `run scenario.yaml` with steps, `--update`, and a non-zero exit on any assertion.
- Golden comparison and geometry assertions over the scenario's artifacts.

**Acceptance**: a scenario file asserts one TUI at two geometries; breaking the
layout fails the run with a first-difference diff and the stage name; `--update`
rewrites the goldens.

## M2 — images

- `render/png`: pure-Go font rasterizer, one cell at a time, no browser.
- `render/chromium`: optional, `rod`-based, for exact CSS/emoji fidelity.
- Pixel-size assertions tied to the geometry (columns × cell width, with the
  tolerance the harness uses), and the artifact written where the caller asked.

**Acceptance**: the same scenario produces a PNG whose measured width is pinned to
its column count — verified by a mutation that narrows the page and fails the check.

## M3 — adapters, parity, adoption

- `adapter/bubbletea`: drive a `tea.Program` in-process (the harness's four
  `TestProgramDriver*` cases).
- The parity checklist in [`parity.md`](parity.md) complete, each row with a test.
- TinyCode's parallel run: both harnesses green, artifacts compared, then the
  harness deleted and CI pointed at the tool (see the replacement criteria there).

**Acceptance**: TinyCode's TUI verification runs through `tuiprobe` alone, and a new
TUI feature is verified by adding a scenario rather than by writing harness code.
