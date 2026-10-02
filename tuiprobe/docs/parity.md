# Parity with TinyCode's TUI harness

`tuiprobe` is only allowed to replace TinyCode's TUI harness when it covers
**every** capability the harness has today. This file is that checklist: one row
per capability, where it lives now, where it will live in the tool, and how the
move is verified.

Source of truth for the "now" column (measured 2026-10-01, commit `200228e`):
`tui/frame_golden_test.go` (420 lines), `tui/frame_shot_test.go` (853),
`tui/pty_screen_test.go` (578), `tui/program_driver_test.go` (251),
`docs/tui-visual-harness.md` (622), plus `tui/testdata/golden/` (27 text frames,
1 ANSI frame).

## Capabilities

| # | Capability (current test) | Needs | tuiprobe home | State |
| --: | --- | --- | --- | --- |
| 1 | Golden text frames (`TestGoldenFrames`) | in-process render | `golden` + app-side scenario | **moved** (engine); scenario stays in the app |
| 2 | Golden ANSI frames (`TestGoldenFrameANSI`) | in-process render | `golden` | **moved** (engine) |
| 3 | Render determinism, twice (`TestFrameScenariosRenderTwice`) | in-process render | `golden.Deterministic` | **moved** — renders twice and fails with the diff |
| 4 | ANSI→HTML clipping (`TestFrameToHTMLClipsToWidth`) | pure function | `screen` (HTML) | **moved** (screen HTML) |
| 5 | Frames fit their geometry (`TestShotScenariosFitTheirGeometry`) | pure function | `golden.Fits` + `golden.AssertFrame` + the `fit` step | **moved** — the CLI step asserts the live session, `AssertFrame` the in-process frame |
| 6 | PNG per scenario + pixel-size bound (`TestFrameScreenshots`) | browser | `render/font` + `render/chromium` + `internal/shot` | **moved** — the PNG and the size assertion are there; the renderer is a pure-Go font rasterizer instead of Chromium, so no browser is needed for the default path |
| 7 | Stage timeout names the stage (`TestRunStageReportsTimeout`) | library | `session.Stage` | **moved** — `StageError` carries the name, the budget and whether it was a timeout, and `ErrStageTimeout` makes it machine-checkable |
| 8 | Cleanup kills before waiting (`TestCleanupLauncherDoesNotWaitForever`) | library | `pty.Close` + `session.Close` | **moved** — the process group is signalled first, then reaped under `CloseGrace` |
| 9 | Real binary on a PTY, styling + double Ctrl+C (`TestBinarySmokeUnderPTY`) | PTY | `pty` + `session` | **engine moved** (`pty`, `session`); the CLI-level check lands with M1 |
| 10 | Size-less PTY must not panic (`TestBinarySmokeWithoutTerminalSize`) | PTY | `pty` (always sets a size) | **moved** — `TestSessionAlwaysGivesTheProgramASize` (zero and negative sizes become 80x24) |
| 11 | Cursor-up repaint (`TestBufferCursorUpRepaints`) | emulator | `screen` | **moved** |
| 12 | Stale text erased (`TestBufferErasesStaleText`) | emulator | `screen` | **moved** |
| 13 | Scroll (`TestBufferScrolls`) | emulator | `screen` | **moved** |
| 14 | Wrapping at the last column (`TestBufferWrapsAtTheLastColumn`) | emulator | `screen` | **moved** |
| 15 | Wide runes (`TestBufferWideRunes`) | emulator | `screen` | **moved** |
| 16 | Queries and modes ignored (`TestBufferIgnoresQueriesAndModes`) | emulator | `screen` | **moved** |
| 17 | Erase display (`TestBufferEraseDisplay`) | emulator | `screen` | **moved** |
| 18 | Replay fidelity, frame → screen (`TestScreenBufferReplaysViewFrame`) | emulator | `screen` | **moved** (literal frames; the app's own case stays) |
| 19 | Screenshot from the live stream (`TestBinaryScreenshotFromStream`) | PTY + emulator + image | `pty` + `screen` + `internal/shot` | **moved** — `shot --name app --out app.png` renders the *live* screen: the daemon's ANSI is replayed through the emulator and drawn, which is the same evidence path the harness had |
| 20 | In-process program driver (`TestProgramDriver*`, 4 tests) | Bubble Tea | `adapter/bubbletea` | **moved** — paints and quits, commands and keys, a resize storm, and a window that reports no size |

Tool-side additions that the harness does not have but the CLI needs: `screen.Render()`
(ANSI output, round-trip tested), the `tuiprobe replay` command, and a real PTY session
engine (`pty` + `session`: bounded stages, kill-before-wait cleanup, screen mirroring,
key encoding, `WaitText`/`WaitStable`, a bounded raw-stream trace).

**Browser discovery and probing moved as well** — the capability the harness grew out
of issues #26 and #43: `CHROME_PATH`/`CHROME`, then the system commands, then the
macOS bundles, then the Playwright cache (headless shell first, newest revision
parsed numerically), each candidate probed with `--version` under a bounded timeout
and its verdict cached for `ProbeRetryAfter` so a timeout is not permanent and a
later installation is still found.

The gated contract moved too: `tuiprobe run --gate VAR` skips with the reason and
exits 0 when `VAR` is unset, and opens nothing — a check that cannot run must say
so rather than look like evidence.

The CLI adds a capability the harness never had — **sessions that outlive one
process** (a daemon on a unix socket: `open`, `send`, `wait`, `text`, `ansi`, `html`,
`trace`, `resize`, `close`, `sessions`, every one of them `--json`, with an
exit-code contract of 0/2/3/4). The harness drove everything inside one test process;
this is what lets an agent look at a screen, press a key and look again.

One behaviour worth knowing when driving a program: a terminal is line-buffered until
the program puts it into raw mode, so a single keystroke only reaches a full-screen TUI
(a real one calls `tcsetattr` at startup). `Send("x")` to a line-oriented program needs
`Send("x", "enter")`; the tool deliberately does not force raw mode, because that would
stop it mirroring what a user's terminal actually does.

## What never moves

These are the project's expectations, not harness machinery. They stay in the
repository no matter how far the extraction goes:

- the scenario fixtures (10 `frame*` builders) and the 28 committed goldens;
- the assertions about *this* UI (status bar, TODO markers, banner rows);
- the Bubble Tea `Model` construction;
- the CI jobs, the gated environment variables and the Makefile target.

## Replacement criteria (all of them, before deleting anything)

1. `tuiprobe` covers rows 1–20 above, each with a test in its own suite.
2. TinyCode runs **both** harnesses in CI and both are green on the same commits
   (the parallel run), with no flake over several consecutive runs.
3. The artifacts match: the 27 text goldens and the 1 ANSI golden are byte-identical
   when produced by the tool, or every difference is explained and deliberately
   re-baselined in a reviewed change; the PNG scenario set and its geometry
   assertions match.
4. Gated behaviour matches: without the gate variable every check skips with a
   reason, and nothing launches a browser, a PTY or a binary in a plain `make test`.
5. The tool is pinned to a released version in TinyCode's `go.mod`, so the harness
   verifies the artifact users get.
6. Only then: delete the harness files, point CI at the tool, and record the new
   workflow in `AGENTS.md` and `CODEBASE.md`.
