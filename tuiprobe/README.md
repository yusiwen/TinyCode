# tuiprobe

See — and assert — what a TUI really draws.

`tuiprobe` drives a terminal program on a real PTY, replays the bytes it emits
through a terminal emulator, and turns the result into artifacts a person, a test
or an agent can read: the screen as text, as ANSI, as HTML, and as a screenshot.
It is language-agnostic — it does not know or care what the program is written in
— and it is also an importable Go library, so a Go project can use the same engine
in-process instead of shelling out.

It exists because writing a TUI means being blind: the thing you are building only
exists when a terminal renders it. The tool's job is to make that rendering
observable, comparable and reproducible, so a change can be verified instead of
described.

## Quick start

```bash
go install github.com/yusiwen/TinyCode/tuiprobe/cmd/tuiprobe@latest

# Turn a captured terminal stream into the screen it produced.
tuiprobe replay --stream capture.bin --size 80x24                 # plain text
tuiprobe replay --stream capture.bin --size 80x24 --format ansi   # with colours
tuiprobe replay --stream capture.bin --size 80x24 --format html   # for a browser
```

Capture a stream from any TUI:

```bash
script -q /dev/null ./myapp > capture.bin      # BSD/macOS
script -q -c ./myapp /dev/null > capture.bin   # util-linux
```

## Status

Early, and being built in the open in
[`yusiwen/TinyCode/tuiprobe`](https://github.com/yusiwen/TinyCode/tree/master/tuiprobe).

| Layer | State |
| --- | --- |
| Terminal emulator (cells, SGR, cursor addressing, scroll, wide runes) | **done** — `screen` |
| Replay a captured stream to text / ANSI / HTML | **done** — `tuiprobe replay` |
| Golden files with `-update`, normalization and first-difference diffing | **done** — `golden` |
| PTY session driver + persistent sessions (`open`, `send`, `wait`, `text`, `close`) | next |
| Scenario runner, geometry assertions, event waits | next |
| Screenshots (pure-Go font rasterizer, optional Chromium renderer) | next |
| Bubble Tea in-process adapter | next |

[`docs/roadmap.md`](docs/roadmap.md) has the milestones and their acceptance
criteria; [`docs/parity.md`](docs/parity.md) tracks the capability-by-capability
parity with TinyCode's own TUI harness, which is the tool's first real consumer.

## Library

```go
import "github.com/yusiwen/TinyCode/tuiprobe/screen"

terminal := screen.New(80, 24)
_, _ = terminal.Write(stream)     // bytes a program wrote to its terminal
terminal.String()                 // plain text screen
terminal.Render()                 // ANSI (round-trips through the emulator)
terminal.HTML()                   // one styled element per cell, for a screenshot
```

```go
import "github.com/yusiwen/TinyCode/tuiprobe/golden"

golden.Assert(t, "testdata/golden/frames", "welcome_80x24.txt", artifact)  // -update aware
```

## Design rules

These are inherited from the harness this tool was extracted from, where each one
was learned by getting it wrong:

- **Bound and name every stage.** A stage that can block runs under its own budget
  and its failure names the stage, so a wedged call is reported rather than hanging.
- **Cleanup kills before it waits**, and bounds the wait.
- **Make timing knobs injectable** — a gated check that cannot be starved on demand
  can only be debugged on CI.
- **The golden is the assertion; the image supports it.** An artifact's dimensions
  are asserted against the geometry it claims to show.
- **Skip with a reason.** A gated check that silently passes is worse than one that
  is absent.
- **A bare LF does not return the cursor to column 0.** The emulator, and anything
  feeding it, must use CRLF.
