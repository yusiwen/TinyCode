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

The steps are `open`, `send`, `wait`, `mark`, `stable`, `sleep`, `golden`, `diff`, `fit`,
`screenshot`, `shot`, `close`, `wait-exit`, `expect-exit`, and a failure names the line and the
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

`prompt-answer` covers the request path without leaving the machine:
`tui/testdata/stub/openai_stub.py` is a minimal OpenAI-compatible endpoint that answers
with one fixed streaming reply, started by the same shell that runs the program (`open --
/bin/sh -c '…stub… & …; kill %1'`), so it dies with the session and needs no lifecycle of
its own. The assertion is the stub's marker: provider reached, SSE parsed, text rendered
— deterministic, no network, no key, no cost.

`tool-call` covers the tool loop with the same stub in "tool" mode: the first request
returns one bash tool call and every later request a final answer, so the loop terminates
after one round. Both halves are asserted — `calling tools: bash` (the call reached the
tool loop) and the final answer being rendered after the result came back. Without the
mode the stub answers identically every time and the agent loops until the step budget;
that is what the first capture of this path showed, steps 6 through 19 all
"calling tools: bash".

Two scenarios cover the approval dialog, which only appears in **build** mode — plan mode
does not ask about writes at all (measured: the same write in plan mode just loops
`calling tools`). `permission-allow` answers `1` (Allow once) and `permission-deny`
answers `4`; both assert the dialog's own wording (`🔒 Write to /tmp/stub-outside-write.txt?`,
`Allow once`) and end on the model's final text, because the stub makes one write call and
then answers. What neither asserts is the file itself — that is the tool's job, not the
terminal's, and the difference between the two runs is outside the screen.

`permission-allow-session` and `permission-allow-always` answer the same dialog with `2`
and `3`. Both persist something beyond the run — that is their point — so each gets its
**own** HOME, which its own shell wipes before the program starts: reusing the shared home
would poison every later scenario (the failure #96 was about), and a per-scenario home that
is never wiped would fail on its *second* run because the rule would already be stored.
They pin that the option is selectable and the loop still finishes; what exactly got stored
is invisible on this screen and belongs to the tool's own tests.

`permission-allow-always` goes one step further, because a permanent grant is a promise
about the *next* run and a screen could not tell one process from the next until `mark` and
`wait --since` existed (issue #126). Its wrapper shell starts the binary twice against one
two-cycle stub, marks the boundary, and asserts with `wait --text stub-final-ok --since`
that the second start reaches the model's final answer with no dialog answered — if the
grant were ignored, the dialog would be drawn, nothing would answer it, and that wait would
time out. The shell also checks the stub's own count (`STUB cycles=2 requests=4`), so a stub
that served one cycle fails the run loudly instead of letting the second start pass on the
first one's answer. `permission-allow-session` has no second-start half: its grant lives in
the session file, and this flow does not resume a session.

`plan-write-no-prompt` is that pair's negative: the same write stub in **plan** mode must
finish on its own, with no key pressed. A screen cannot assert an absence any other way —
under build mode the identical call raises the dialog and waits for a human, so reaching
the model's final text and exiting 0 here is the evidence that nothing asked. It does not
claim whether plan mode denies the write or skips it silently; that distinction is
invisible on this screen and belongs to the tool's own tests.

A further, `live-answer.scenario`, makes a **real provider call** through this machine's
`~/.tinycode/.env`: `make test-tui-live`, gated by `TINYCODE_LIVE`, never run by CI.
When it fails, read the error before blaming the key — `dial tcp` or
`TLS handshake timeout` is the network, `401` is the key.

`make test-tui-scenarios` wipes `/tmp/tinyscen-home` before it seeds the session fixture —
scenarios share that home, so a leftover from an earlier run changes the world a later
one sees (issue #96). A **manual** `tuiprobe run` does not wipe anything: if a scenario
behaves differently by hand than in the suite, reset that directory first.

### 3. A tool change

If the capability that is missing is generic, it belongs in `tuiprobe/`: another step,
a renderer, a better diff. Add it there, cut a `tuiprobe/v*` release, then bump the pin
in `go.mod`. A new local emulator, PTY wrapper or golden helper in this repository is
the signal that this route was the right one.

`exit-command` covers the documented way out that is neither Ctrl+C nor closing the
terminal: `/exit` is two lines of code (`persistSession()`, then `tea.Quit`) and the
scenario ends on a command rather than a key chord. Its readiness wait is what makes it
work at all — an earlier attempt typed the command before the input row accepted keys, lost
it, and timed out, which is the flake in #102.

`fork-command` covers `/fork`, which appends a system message rather than opening a picker
— an earlier capture only looked like a picker because a `/session` list was still on screen.
A fresh session has no branch, so it asserts the "No active session to fork" literal from
`tui/update.go:1119` and then leaves with `/exit`.

`diagnostics-command` covers `/diagnostics` when no language server is running — the
outcome that needs no gopls, so it runs everywhere. The listing it can print instead is
deliberately **not** asserted: its text cannot be read without gopls, and writing an
assertion for a string nobody has seen is the mistake this suite keeps catching. That one
belongs to a gated line that runs where gopls exists.

`lsp-diagnostics` is one of the gated scenarios: it runs with `LSP_TEST=1` via
`make test-tui-lsp` (CI's `lsp` job, which already provides gopls) and asserts the
`/diagnostics` outcome `No LSP diagnostics.` — a string only reachable when a client exists.
The server is started the way a user starts it, by reading a file, so the scenario is also
the regression guard for issue #111: while that circle existed, the warmup never ran and the
status line read "LSP not available". The listing outcome would need real diagnostics to be
present and is not covered.

`lsp-tools` reaches the same status line from the other side, and is the regression guard for
issue #114. The stub asks for `lsp_symbols` — the name `lsp.ToolFactory` registers, because a
stub that invents a tool name gets `unknown tool: …` back from the agent loop, and a log that
records only the result's size cannot tell that apart from an LSP error — and nothing in the
scenario reads or writes a file. `No LSP diagnostics.` is therefore reachable only if the
tool call itself promoted the session's server. Before the fix the tool started a server of
its own that the session never heard about, so the line read "LSP not available", and with no
workspace configured at all the per-call server was rooted at the file and gopls answered
`LSP error 0: no views` — the shipped default, since `main.go` registers the four LSP tools
whether or not `lsp.enabled` is set.

Both `lsp-*` scenarios wipe their throwaway `HOME` with `chmod -R u+w … 2>/dev/null; rm -rf …`
rather than a bare `rm -rf`, and keep that wipe outside the `&&` chain that starts the stub.
gopls resolves the workspace through that `HOME` and leaves a read-only Go module cache under
`$HOME/go/pkg/mod`; `rm -rf` fails on it, the chain stops before the stub starts, and the
second run of the same scenario dies on `connection refused` (issue #115). CI never saw it —
its `/tmp` starts empty — which is why the reproduction is "run `make test-tui-lsp` twice".

## Typing waits for the input

Every scenario that types waits for the input row (`wait --text "Type your request"`)
before its first keystroke, and that wait sits **after** the `open` step. The welcome
heading appears before the input is ready, and keys sent into that gap can be lost — one
CI run lost a `/theme` that way, which is issue #102. The first attempt at this fix
inserted the wait before `open` in the one scenario without a greeting line and failed with
`daemon: no session named "default"`, which is the same "a step must come from the flow it
belongs to" mistake, one level down.

## A rule this suite keeps re-learning

A step or a string written into a scenario must come from the flow that scenario drives.
Four assertions here were wrong that way, each caught by the scenario failing rather than by
review: `[builtin]` compiled as a regex character class and matched almost anything; grey
`136;136;136` was treated as a repeated SGR parameter and became another colour;
`calling tools: write_file` was borrowed from the retry-after-denial loop, which is not the
flow a first write takes; and Ctrl+C was sent to a one-shot program that had already
answered and left, which failed as `write /dev/ptmx: input/output error`. When an assertion
does not fit, delete it and find the flow's own string — do not adjust it until it passes.

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
