# TUI verification

How this project checks that its terminal user interface still looks and behaves the
way it should. The mechanism lives in **[tuiprobe](../tuiprobe/README.md)**, a nested
module released as `tuiprobe/v0.1.0` and pinned in `go.mod`; this repository keeps the
fixtures and the judgments.

## The layers

| Layer | What it proves | Where the assertion lives |
| --- | --- | --- |
| Frame golden | the exact text of a screen | `golden.Assert` over `tui/testdata/golden/frames/*.txt` |
| ANSI golden | colour, bold, underline, OSC 8 links survive | `golden.Assert` over `tui/testdata/golden/ansi/markdown_80x24.ansi` |
| Determinism | a second `View()` is identical (dirty-tracking leaks would show) | `golden.Deterministic` |
| Geometry | every row, clipped the way the renderer clips it, fits its terminal | `golden.Clip` + `golden.FitsWidth` |
| Image | what the screen looks like, at the size its geometry allows | `tuiprobe/render/font` (pure Go; Chromium optional) |
| Real binary | `main.go` wiring, the real renderer, a real terminal, styling, double Ctrl+C | `tuiprobe/session` on a PTY |
| Live stream | what the program actually painted, replayed and rendered | `session.Trace` into `tuiprobe/screen` |

The first four run in the ordinary `ci` job. The last three are gated by `TUI_SHOT=1`
and run in the `tui-visual` job, together with the browser smoke test.

## Running it

```bash
go test ./tui -count=1                  # frame goldens, ANSI, determinism, geometry
make build && make test-tui-visual      # + images and the PTY smoke (TUI_SHOT=1)
go test ./tui -run Golden -update       # after an intended layout change
```

`TUI_SHOT_DIR` chooses where images are written (default the system temporary
directory). `make test-tui-visual` runs verbosely on purpose: which gated check ran and
which skipped, with the reason, is the point of that job.

## Adding a screen

One entry in the scenario table in `tui/scenarios_test.go`:

```go
{"myscreen", []golden.Size{{W: 80, H: 24}}, []shotSpec{{"myscreen", golden.Size{W: 80, H: 24}}}, frameMyScreen},
```

plus the builder that produces the model, then `go test ./tui -run Golden -update` to
commit the new frame and read the regenerated PNG to check it looks right.

## What this project still owns, and why

- **The builders** (`frameModel`, `frameWelcome`, …): they construct `*TuiModel` with
  the project's own API and content. A general tool cannot know how to build this
  application's screens.
- **The expectations**: the 27 text frames and the ANSI frame are what *this* product
  considers correct. The tool offers `-update`, normalization and a first-difference
  diff; it cannot know what the welcome banner should say.
- **The judgments**: that the status bar's mode label is `plan`, that the banner art is
  dropped at 40 columns, that at least one scenario must be wider than its terminal so
  the clip stays tested.
- **The gating and CI wiring**: which checks need a PTY or a browser, and what the
  annotation baseline is.

## Known limits

- Glyph coverage in images: the pure-Go renderer uses Go Mono and draws a visible
  placeholder for a rune the face lacks (box drawing, braille, icons). Pass `--font`
  to `tuiprobe shot` with a face that has them.
- The Chromium renderer's screenshot size is a range (`cols*9 .. cols*9+24` px), not a
  number; the default font renderer is exact.
- A frame may be wider *and* taller than its terminal — the status bar is drawn outside
  the cell grid, and bubbletea truncates each line to the window before writing it. The
  golden stores the frame as `View()` returns it; the geometry assertion applies the
  same clip the renderer does (issue #25).

## History

This verification used to be a harness inside `tui/`: its own terminal emulator, PTY
scaffolding, SGR→CSS renderer, Chromium plumbing and golden helpers — about 1,400
lines of mechanism beside 242 lines of fixtures. All of it moved to `tuiprobe`, which
was proven against this repository before anything was deleted: the 27 text goldens and
the ANSI golden are reproduced **byte for byte** through the same builders, and both
suites ran green together while both existed. The capability-by-capability record is
[tuiprobe/docs/parity.md](../tuiprobe/docs/parity.md).
