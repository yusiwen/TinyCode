# TUI verification

How this project checks that its terminal user interface still looks and behaves the
way it should. The mechanism lives in **[tuiprobe](../tuiprobe/README.md)**, a nested
module pinned in `go.mod`; this repository keeps the
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

## Adding a scenario

Three routes, in the order to try them. `AGENTS.md` carries the short version.

### 1. A frame in the suite (default)

Add a builder to `tui/scenarios_test.go` and one entry to the scenario table:

```go
{"myscreen", []golden.Size{{W: 80, H: 24}}, []shotSpec{{"myscreen", golden.Size{W: 80, H: 24}}}, frameMyScreen},
```

`sizes` lists the geometries whose *text* is committed; `shots` lists the images worth
looking at. Then:

```bash
go test ./tui -run Golden -update                 # writes the golden(s)
make build && TUI_SHOT=1 go test ./tui -count=1   # images land in TUI_SHOT_DIR
```

The image is not optional reading: the golden proves the text did not change, the
picture is the only thing that shows it is right. An intended layout change and the
regenerated PNG belong in the same commit.

### 2. A black-box scenario (interaction, or the real binary)

For a property about `main.go` wiring, flags, an interactive sequence or a live stream
rather than one frame, add a scenario file under `tui/testdata/scenarios/` and run it
with the pinned tool:

```bash
make test-tui-scenarios                # every scenario file, gated by TUI_SHOT
go run github.com/yusiwen/TinyCode/tuiprobe/cmd/tuiprobe run \
  --gate TUI_SHOT --dir . tui/testdata/scenarios/startup.scenario
go run github.com/yusiwen/TinyCode/tuiprobe/cmd/tuiprobe run \
  --dir . --update tui/testdata/scenarios/startup.scenario   # re-baseline
```

The command takes its version from `go.mod` (no `@version`), so bumping the pin is the
only place a tool version appears.

The steps are `open`, `send`, `wait`, `stable`, `sleep`, `golden`, `diff`, `fit`,
`screenshot`, `close`, `wait-exit`, `expect-exit`, and a failure names the line and the
step; see [tuiprobe/docs/scenario.md](../tuiprobe/docs/scenario.md). The runner lives in
`tuiprobe/internal/scenario`, so it is reachable as a command, not as a library — run
the files with `make test-tui-scenarios`, which is what the `tui-visual` job does.
`--gate TUI_SHOT` makes each file a no-op that prints why it skipped.

Three files today:

- `startup.scenario` — the real binary at 80x24, the startup frame, `fit --size 80x24`,
  the documented double Ctrl+C, and the exit code read from the program's own exit
  (`wait-exit`) rather than from having to kill it;
- `narrow.scenario` — the same at 40x12, where the banner is dropped and the status bar
  is wider than the terminal, plus a screenshot;
- `palette.scenario` — `/` opens the command palette, typing filters it, Esc closes it,
  and the double Ctrl+C leaves (Esc first: Ctrl+C with the palette open closes the
  palette instead of quitting).

Three more cover the paths a frame golden cannot: `help-command` (`/help` prints the
command list — input row, dispatch and scrollback on a real terminal), `mode-switch`
(`/build` moves the status bar to `⚡ build`; the welcome banner also contains the word
"build", so the assertion is the mode indicator), and `version-flag`
(`bin/tinycode --version` answers and exits 0, the only non-interactive path in the
suite).

Three more add the command surface and the input box: `theme-command` (`/theme` prints
"Available themes: default, nord" — a local command with an argument-less form),
`input-editing` (type, Ctrl+J, type again: both lines must be on screen, so a
single-row input that silently drops the rest fails here), and `list-sessions`
(`--list-sessions` answers and exits 0 against a throwaway session directory; the
assertion is a session id rather than the "Available sessions:" header, because the
list can be longer than the terminal and the header is its first line).

Three more finish the argument-less command family: `model-picker` (`/model` lists the
providers with a hint line), `skill-list` (`/skill` lists the discovered skills), and
`plan-mode` (`/build` then `/plan`, asserting the status-bar indicator each way).
Their patterns are escaped where the expected text contains regex metacharacters —
`[builtin]` as a bare pattern is a character class that would match any screen, which is
the kind of assertion that passes for the wrong reason.

One scenario's input is a *file* rather than a keystroke: `resume-fixture` starts the
binary with `--resume=TUI-20260101-000000`, against a committed session file under
`tui/testdata/fixtures/` that `make test-tui-scenarios` copies into the throwaway HOME.
Its assertion is a marker string the fixture carries, so the resume path is checked
against known content instead of whatever earlier runs left behind.

A further, `live-answer.scenario`, makes a **real provider call** through this machine's
`~/.tinycode/.env`: `make test-tui-live`, gated by `TINYCODE_LIVE`, never run by CI.
When it fails, read the error before blaming the key — `dial tcp` or
`TLS handshake timeout` is the network, `401` is the key.

### 3. A tool change

If the capability that is missing is generic, it belongs in `tuiprobe/`: another step,
a renderer, a better diff. Add it there, cut a `tuiprobe/v*` release, then bump the pin
in `go.mod`. A new local emulator, PTY wrapper or golden helper in this repository is
the signal that this route was the right one.

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
