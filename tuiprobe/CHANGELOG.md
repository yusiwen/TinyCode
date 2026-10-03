# Changelog — tuiprobe

The tool's own history. TinyCode's changelog lives at the repository root.

## v0.1.2 — 2026-10-03

- **`screen`: an escape sequence split across `Write` calls is no longer torn in half**
  (issue #75). Terminal output arrives in arbitrary chunks and a boundary can fall
  inside an SGR or OSC sequence; the leading half was dropped and the trailing half
  drawn as text, so a live capture read `github.com/yusiw5;255;4men/Tin` while a
  single-write replay of the same bytes was perfect. The unfinished tail is carried to
  the next write, and the test splits the exact stream at every byte boundary.
- **`session`: the queries a program asks before it will paint are answered**
  (issue #76). TinyCode writes OSC 11 and CSI 6n at startup and then paints nothing
  until they time out — about five seconds measured — because a real terminal answers
  both. OSC 11 now gets the configured background (`Options.Background`, four hex
  digits per channel, default `#1c1c1c`) and CSI 6n the session's own cursor;
  `Options.NoQueryAnswers` (or `TUIPROBE_ANSWER_QUERIES=0`) turns it off so the
  opposite — a program that must cope with a silent terminal — can still be tested.
- `screen.Buffer.Cursor()` reports the cursor position, which is what CSI 6n needs.

## v0.1.1 — 2026-10-03

- **`wait-exit` step**: waits, bounded, for the program to end on its own and remembers
  its own exit code. The first scenario file written against TinyCode found the gap:
  `expect-exit` right after a `send` closed — and therefore killed — a program that was
  leaving by itself, reporting `-1` for something that exits `0`. The CLI verb and the
  daemon command already existed; the scenario vocabulary now matches them.
- Documented in `docs/scenario.md` with the step table.

## v0.1.0 — 2026-10-02

First release: everything TinyCode's TUI harness could do, in a form any project can
use — and the parity run that proved it.

### The library

- **`screen`** — the terminal emulator (cells, SGR, cursor addressing, scroll, wide
  runes, erase), plus `Render()` for ANSI output. An app's frame and a live PTY stream
  both end up here, which is what makes `Text()` mean the same thing on every path.
- **`pty`** — spawn any command on a real terminal: the size is applied *before* the
  child runs, resize goes through SIGWINCH, the stream ends on EIO, and cleanup
  signals the process group first and then waits, bounded. A zero or negative
  geometry becomes 80x24, never a 0x0 terminal. `TerminalEnv` builds the hermetic
  environment a terminal application expects, and a bare `KEY` in `Env` removes a
  variable (`NO_COLOR=1` in an agent shell otherwise turns every style off).
- **`session`** — send keys (`enter`, `ctrl+c`, `alt+left`, `f1`…), read the screen as
  text/ANSI/HTML, `WaitText`, `WaitStable`, `Trace`, and `Stage`: every step has a
  name and a budget, and a step that overruns says so.
- **`golden`** — golden files with `-update`, normalization, first-difference diffing,
  `Deterministic` (render twice, be identical), `Fits`/`FitsWidth`, `Clip` (a frame
  may be wider and taller than its terminal — the status bar is drawn outside the cell
  grid), and `Assert`/`AssertFrame`.
- **`render/font`** — PNGs with no browser: the cell grid drawn with an embedded face,
  exact arithmetic (`columns × cell width` by `rows × cell height`), a visible
  placeholder for a rune the face lacks, and `--font` for glyphs Go Mono does not
  carry.
- **`render/chromium`** — the optional browser renderer, with the discovery that was
  learned the hard way: `CHROME_PATH`/`CHROME`, system commands, macOS bundles, the
  Playwright cache (headless shell first, revisions parsed numerically), every
  candidate probed with `--version` under a bounded timeout, and the verdict cached
  with an expiry so a timeout is not permanent.
- **`adapter/bubbletea`** — drive a `tea.Program` in-process, with its output going
  through the same emulator as the PTY path.

### The command line

```
tuiprobe open|send|wait|text|ansi|html|trace|resize|close|wait-exit|sessions
tuiprobe shot --renderer font|chromium
tuiprobe replay --stream <file> --format text|ansi|html
tuiprobe run [--update] [--gate VAR] <scenario>
tuiprobe diff --name app --against <file>
```

Sessions live in a daemon on a unix socket: the first command starts it, it exits on
its own once idle, and every refusal is classified as an exit code (2 usage or
assertion, 3 timeout, 4 unknown session). Scenarios are text files with steps
(`open`, `send`, `wait`, `stable`, `sleep`, `golden`, `diff`, `fit`, `screenshot`,
`close`, `expect-exit`), quoted arguments, and a gate that skips with its reason.

### Proven against TinyCode

`tui/tuiprobe_parity_test.go` renders TinyCode's own scenarios with TinyCode's own
builders and asserts them through tuiprobe, in the same test run as the harness:

- 27 committed text goldens reproduced **byte for byte**;
- the committed ANSI golden **identical**, escapes included;
- determinism across two renders for all 27;
- 8 scenario PNGs plus one from the live PTY stream, each exactly the geometry it
  claims;
- the real binary on a PTY: starts, paints SGR, quits on the documented double Ctrl+C;
- a requested 0x0 terminal upgraded rather than passed on.

The harness's own gated tests pass in that same run, locally and in CI.

### Known limits

- The faithful-glyph problem is not solved by magic: Go Mono carries no box-drawing,
  braille or icon glyphs, so those want `--font`.
- Chromium's screenshot size is asserted as a range (`cols*9 .. cols*9+24` px), not a
  number.
- Linux and macOS only: the reaping path uses process groups and PTY semantics;
  Windows would need ConPTY.
- Scenarios have no loops, variables or conditionals by design. When logic is needed,
  call the CLI from the language being tested — the CLI is the API.
