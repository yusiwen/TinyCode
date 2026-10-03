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

# Drive a program on a real terminal, one command at a time. The first command
# starts a small daemon so the session survives between invocations; it exits on
# its own once idle.
tuiprobe open --name app --size 100x30 -- ./myapp
tuiprobe wait --name app --text 'ready' --timeout 5s
tuiprobe text --name app                  # the screen as text
tuiprobe send --name app --text '/' --key ctrl+p --key enter
tuiprobe wait --name app --stable 200ms   # let the repaint settle
tuiprobe trace --name app --n 2000        # the raw stream, for diagnosis
tuiprobe close --name app                 # end it now; the exit code is included
tuiprobe wait-exit --name app             # let it end on its own, then read its code

# Or assert a whole interaction from a file, the way CI does.
cat > welcome.scenario <<'SCENARIO'
open --size 80x24 -- ./myapp
wait --text "ready" --timeout 5s
golden testdata/golden/welcome_80x24.txt
send --text ":help" --key enter
wait --text "Help"
golden --ansi testdata/golden/help_80x24.ansi
close
expect-exit 0
SCENARIO
tuiprobe run --dir . --update welcome.scenario   # write the goldens once
tuiprobe run --dir . welcome.scenario            # then compare them

# Or take a picture of it — no browser involved.
tuiprobe shot --name app --out app.png --scale 2                  # 2x pixels
tuiprobe shot --name app --out app.html --format html
tuiprobe shot --name app --out app.png --renderer chromium          # through a browser

# Or replay a captured stream offline.
tuiprobe replay --stream capture.bin --size 80x24 --format ansi
```

Every session command takes `--json`, and the socket comes from `--socket` or
`$TUIPROBE_SOCKET`.

Capture a stream from any TUI:

```bash
script -q /dev/null ./myapp > capture.bin      # BSD/macOS
script -q -c ./myapp /dev/null > capture.bin   # util-linux
```

## Status

**Parity with TinyCode's harness is measured, not asserted**: the 27 committed text
goldens and the 1 ANSI golden are reproduced **byte for byte** through the same
builders, and both suites pass in the same test run. See
[docs/parity.md](docs/parity.md) for the comparison and the three things the run
found missing.

Early, and being built in the open in
[`yusiwen/TinyCode/tuiprobe`](https://github.com/yusiwen/TinyCode/tree/master/tuiprobe).

| Layer | State |
| --- | --- |
| Terminal emulator (cells, SGR, cursor addressing, scroll, wide runes) | **done** — `screen` |
| Replay a captured stream to text / ANSI / HTML | **done** — `tuiprobe replay` |
| Golden files with `-update`, normalization and first-difference diffing | **done** — `golden` |
| PTY driver (spawn, size, resize, bounded reap) | **done** — `pty` |
| Session engine (send keys, screen text/ANSI/HTML, `WaitText`, `WaitStable`, trace) | **done** — `session` |
| Sessions a CLI invocation can share: daemon + unix socket, `open`/`send`/`wait`/`text`/`ansi`/`html`/`trace`/`resize`/`close`/`sessions`, `--json`, idle exit | **done** — `internal/daemon`, `tuiprobe <command>` |
| Scenario files (`tuiprobe run`), golden diff with `--update`, geometry assertions, gated skip-with-reason | **done** — `internal/scenario`, see [docs/scenario.md](docs/scenario.md) |
| Named, bounded stages (`session.Stage`) and kill-before-wait cleanup | **done** — `session`, `pty` |
| Screenshots: pure-Go font rasterizer (`shot --name app --out app.png`), PNG size asserted against the geometry | **done** — `render/font`, `internal/shot` |
| Chromium renderer + browser discovery/probe | **done** — `render/chromium` (`--renderer chromium`) |
| Bubble Tea in-process adapter (`adapter/bubbletea`) | **done** |

[`docs/roadmap.md`](docs/roadmap.md) has the milestones and their acceptance
criteria; [`docs/parity.md`](docs/parity.md) tracks the capability-by-capability
parity with TinyCode's own TUI harness, which is the tool's first real consumer.

## Library

```go
import tea "github.com/yusiwen/TinyCode/tuiprobe/adapter/bubbletea"

prog, _ := tea.Start(myModel, tea.Options{Cols: 80, Rows: 24})
_ = prog.Send(tea.KeyMsg{Type: tea.KeyUp})
_ = prog.WaitText("count=1", 5*time.Second)
prog.Text()   // what a user would see: the output goes through the emulator
```

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

## Rendering notes

The default renderer draws the cell grid with Go Mono (embedded in
`golang.org/x/image`), so PNGs need no browser, no font file and no network. Its
arithmetic is exact: an image is `columns × cell width` by `rows × cell height`, and
`shot` refuses to write one that is not — an image whose size is not a function of
the geometry is not evidence of that geometry.

`--renderer chromium` drives a real browser instead (`--browser` picks one,
otherwise `CHROME_PATH`, `CHROME`, a system Chromium/Chrome/Edge or the Playwright
cache is probed with `--version`; the headless shell is preferred because it is the
build that can take a screenshot, and a probe verdict expires so a browser installed
later is found). It costs a browser launch — about 2.8 s here — and buys system fonts
and exact CSS. Its screenshot size cannot be predicted to the pixel, so it is asserted
against the range the geometry allows (`cols*9 .. cols*9+24` px) rather than an exact
number. The layout is absolute per cell, because a browser only behaves like a
terminal grid when every cell is told where it is.

The trade-off is glyph coverage: Go Mono has no box-drawing, braille or icon
glyphs, and a rune the face lacks is drawn as a visible placeholder rather than
dropped. Point `--font /path/to/JetBrainsMono.ttf` (or any monospace TTF) at a font
with the glyphs your UI uses. `Pictures` of a TUI full of box drawing want that;
a text-heavy screen does not need it.

## Releases

`tuiprobe/v*` tags are its releases (`tuiprobe/v0.1.0` was the first, `v0.1.1` added the scenario `wait-exit` step); the workflow
cross-compiles linux/amd64, linux/arm64 and darwin/arm64 and attaches them, with the
version injected into the binary. See [CHANGELOG.md](CHANGELOG.md).

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
