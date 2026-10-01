# TUI Visual Harness

Reference for the visual test tooling in `tui/`: what each layer proves, how it is
implemented, and how to run or extend it. The code lives in three test files and
is gated by environment variables, so an ordinary `make test` stays fast and
needs neither a browser nor a terminal device.

| File | Role |
| --- | --- |
| `tui/frame_golden_test.go` | Renders the model's `View()` and pins the frame as committed text |
| `tui/frame_shot_test.go` | Turns a frame into PNG through headless Chromium; runs the built binary on a real PTY |
| `tui/pty_screen_test.go` | Terminal emulator that replays a live PTY stream, and the tests built on it |

The harness exists because a TUI regression is a *visual* change: a wrong column,
a lost style, a repaint that never lands. Assertions on strings catch some of it;
an image you can look at catches the rest.

References of the form `#N` point at this repository's GitHub issues.

---

## 1. What it proves

Four independent layers, from cheapest to most faithful:

| Layer | Question it answers | It cannot show |
| --- | --- | --- |
| Golden frames | Did the layout change? (wrapping, alignment, overflow, status bar) | Colours (stripped), and anything the real renderer does with them |
| PNG screenshots | What does the frame *look* like? | Live values (counts, clock) |
| PTY smoke | Does the real binary start, paint, carry colour and quit? | Any specific layout beyond the startup frame |
| Stream replay | Does the running renderer's byte stream produce the frame on a real terminal? | Everything after the startup frame (no byte golden of a live screen) |

The layers are deliberately redundant: the golden is the regression *assertion*,
the PNG is the evidence a reviewer (or an agent) can open, and the PTY layers
guard the wiring between `main.go`, the renderer and the kernel that the
frame functions never touch.

---

## 2. Quick start

```bash
# 1. Always-on layer: golden frames (no browser, no binary, no PTY)
go test ./tui -run Golden -count=1

# 2. Regenerate the goldens after an intended layout change
go test ./tui -run Golden -update

# 3. Everything gated: PNGs + the real binary on a PTY
make test-tui-visual                    # builds bin/tinycode first
TUI_SHOT_DIR=/tmp/tui-shots make test-tui-visual   # collect the PNGs elsewhere
```

`make test-tui-visual` runs:

```bash
TUI_SHOT=1 go test -count=1 -timeout 5m -run 'TestFrameScreenshots|TestBinary' ./tui/
```

`TUI_SHOT=1` is the only gate; without it every gated test calls `t.Skipf`, so
`make test` and `go test ./...` never launch Chromium and never need `/dev/ptmx`.
The two bounds tests (`TestRunStageReportsTimeout`,
`TestCleanupLauncherDoesNotWaitForever`) are *ungated* and run in the `ci` job,
because they pin the harness's own timeouts and need no browser.

The PNGs land in `TUI_SHOT_DIR`, or `/tmp` when that is unset (falling back to
the test's temp dir if `/tmp` does not exist). Names:

```
tinycode-frame-<scenario>-<W>x<H>.png    # one per screenshot scenario
tinycode-frame-<scenario>-<W>x<H>.html   # the page it was captured from
tinycode-pty-welcome-80x24.png           # replayed from the live PTY stream
```

PNGs are never committed; they are review artifacts. The intermediate `.html`
is written next to each PNG because it is the cheapest thing to inspect when a
screenshot looks wrong (it is the exact document Chromium rendered).

---

## 3. Architecture and data flow

```
frameScenarios ─┬─ View() ──► normalizeFrame ──► assertGolden ──► testdata/golden/frames/*.txt
                │                                             └─► testdata/golden/ansi/*.ansi (one raw frame)
                └─ View() ──► frameToHTML ──► capturePNG ──► Chromium ──► tinycode-frame-*.png

bin/tinycode ──► PTY (80x24, or no size) ──► raw byte stream ─┬─► assertions on SGR / clean exit
                                                              └─► screenBuffer ──► HTML ──► capturePNG ──► PNG
```

Both screenshot paths converge on the same page template (`htmlDocument`) and
the same capture function (`capturePNG`), so an image looks the same whichever
way its body was produced.

---

## 4. Layer 1 — Golden frames

### Entry points

- `TestGoldenFrames` — every scenario at every geometry it declares; compares the
  normalized plain text against `tui/testdata/golden/frames/<name>_<W>x<H>.txt`.
- `TestGoldenFrameANSI` — one raw frame (`markdown_80x24`) written **with** its
  escape sequences to `tui/testdata/golden/ansi/`, so colour, bold, underline and
  OSC 8 hyperlinks stay diffable instead of being stripped by the plain-text form.
- `TestFrameScenariosRenderTwice` — calls `View()` twice on one model and requires
  identical output; a diff means the incremental CellGrid path leaks dirty state
  between frames.

Current inventory: **9 scenarios → 27 text frames + 1 ANSI frame**.

### Pinning the renderer

`withTrueColor(t, fn)` sets the lipgloss colour profile to `termenv.TrueColor`
for the duration of a render and restores it afterwards. Without it lipgloss sees
a non-TTY stdout, falls back to the Ascii profile, and every style silently
vanishes from the frame — which is exactly the state earlier assertions were
blind to.

The profile change also requires `resetStyleCache()`: the `CellStyle` →
`lipgloss.Style` cache stores styles built under the previous renderer state, so
a stale entry would keep the old profile's output. Both the set and the restore
empty the cache.

`TestGoldenFrames` additionally fails if a frame contains no `\x1b[` at all: a
frame with zero SGR sequences means the profile was lost, not that the frame
happens to be plain.

### Normalizing

`normalizeFrame` is what makes a golden reviewable:

1. drop OSC sequences (ESC `]` … BEL or ST) — the welcome banner carries its
   hyperlink target that way and `stripANSIView` only handles CSI;
2. strip the remaining CSI sequences via `stripANSIView`;
3. `\r\n` → `\n`;
4. trim trailing blanks from every line;
5. drop trailing blank lines, and end with exactly one newline.

Two frames that differ only in fixed-width padding therefore compare equal, and
the golden shows the content a human cares about instead of a wall of spaces.

`frameDiff(want, got)` reports the first differing line with one line of context
and the first differing column, so a failure reads like a review comment.

### Regenerating

`assertGolden` compares by default and **fails on a missing file** with the hint
to regenerate — a test run must never silently invent the baseline it checks
against. `-update` rewrites the frame files instead:

```bash
go test ./tui -run Golden -update
```

Review the diff before committing: `-update` records whatever the renderer
produced, including a regression. `frame_shot_test.go` registers the same flag,
so an `-update` run and a screenshot run share one switch.

### Scenario builders and geometry tiers

`frameModel(w, h)` builds a ready model without the real constructor (as the
other layout tests do). It deliberately leaves `sessionStart` at the zero time:
`time.Since(zero)` saturates at the maximum duration, so the status bar's session
clock renders a constant string instead of a wall-clock value a golden could
never match.

Builders are small and single-purpose: `frameWelcome`, `frameMarkdown`,
`frameStreaming`, `frameTodo`, `frameDialog`, `framePalette`,
`frameDiagnostics`, `frameCompressing`, `frameLongOutput`.

`frameScenarios` pairs each builder with the geometries worth pinning:

| Scenario | Sizes |
| --- | --- |
| welcome | 80x24, 120x40, 100x30 |
| markdown | 80x24, 120x40, 200x50, 100x30, 40x12 |
| streaming | 80x24, 120x40 |
| todo | 80x24, 120x40, 40x12 |
| dialog | 80x24, 120x40, 40x12 |
| palette | 80x24, 120x40, 40x12 |
| diagnostics | 80x24, 120x40 |
| compressing | 80x24, 40x12 |
| longoutput | 80x24, 120x40, 200x50, 100x30 |

`200x50` is reserved for the two scenarios that exercise wrapping and overflow,
`100x30` for a wide-but-not-extreme layout, and `40x12` for the narrow end where
the banner art is dropped and tables must still line up.

### A golden is the model's frame, not the screen

`View()` at the narrow end can be taller than the terminal (the palette at 40x12
is) or wider than it (the status bar at 40 columns): the renderer truncates and
scrolls to the real geometry. The golden pins what `View()` returned; the
*screen* is what layer 4 shows.

---

## 5. Layer 2 — PNG screenshots (gated)

`TestFrameScreenshots` renders eight scenarios at 2x through headless Chromium
and writes one PNG each.

### Frame → HTML

The TUI's `View()` is a flat string of rows — it never uses cursor addressing —
so a tiny SGR→CSS renderer is enough (`frameToHTML`):

- `ansiState` holds the subset the frame can carry: bold, dim, italic, underline,
  foreground and background.
- `applySGR` folds SGR parameters into that state; `ansiPalette` maps the 16 base
  colours and `xterm256ToCSS` expands indices 16–255 (6×6×6 cube and greys).
- CSI sequences are consumed through their final byte and dropped unless the
  final byte is `m`; OSC hyperlinks are skipped entirely (the image carries no
  link target, unlike the raw ANSI golden); `\r` is dropped.
- Runs of identically styled text become one `<span style="…">`.

`htmlDocument` wraps the body in the terminal-looking page (dark background,
monospace stack, `white-space: pre`) used by **both** screenshot paths.

### Browser

`tool.FindBrowser()` resolves a usable Chromium/Chrome and is shared with the web
extraction tools, so the search order cannot drift:

1. `CHROME_PATH`, then `CHROME` (what `browser-actions/setup-chrome` publishes);
2. `chromium-browser`, `chromium`, `google-chrome`, `google-chrome-stable`,
   `chrome` on `PATH`;
3. the Playwright cache under `$HOME` (`~/Library/Caches/ms-playwright` on macOS,
   `~/.cache/ms-playwright` elsewhere).

Every candidate is probed with `--version` under a 5 s bound, so a broken
launcher is skipped rather than handed to the harness (the probe is memoized per
process — see #26 for its load sensitivity). When discovery returns nothing, the
test skips with the reason instead of failing.

The launcher is built by `newShotLauncher`: the discovered binary, headless, a
throwaway profile in `TUI_SHOT_DIR/tinycode-shot-profile-<pid>`, `--no-sandbox`
(a GitHub runner and most sandboxes deny Chromium a user namespace),
`disable-gpu`, `disable-dev-shm-usage` and `hide-scrollbars`.

`connectBrowser` connects over the launcher's debug WebSocket and logs the
browser version — the first thing worth knowing when a gated run behaves
differently on another machine.

### Capture

`capturePNG(browser, dir, name, document, size)`:

1. writes `name.html`;
2. opens a page, and sets the viewport to `W*9 × H*19` at `DeviceScaleFactor: 2`
   (a fixed per-cell estimate: the text stays readable when zoomed in to inspect
   a column alignment);
3. navigates `file://`, waits for load, captures a full-page screenshot;
4. writes `name.png`;
5. rejects a PNG under 10 000 bytes ("the page probably rendered empty"), one
   without the PNG magic bytes, and one whose width is outside
   `W*9 .. W*9+24` CSS px — the geometry plus the page's fixed 12 px of
   horizontal padding.

It uses rod's error-returning API (`Page`, `SetViewport`, `Navigate`, `WaitLoad`,
`Screenshot`) and never `Must*`: a panic inside a subtest goroutine skips the
deferred `browser.Close()`, and the `t.Cleanup` that follows then blocks.

### Bounds

Every stage that can block runs under `runStage`, which executes it on a
goroutine and reports `no result within <budget> (a wedged CDP call; the stage
goroutine stays blocked)` if `shotTimeout` elapses. The caller names the stage
(`screenshot todo: …`), so a failure is attributed instead of being buried in a
goroutine dump five minutes later.

`shotTimeout` (default 20 s) is a **variable** on purpose: lowering it is how the
CI hang was reproduced locally (see §9). Each screenshot takes a fresh
`browser.Timeout(shotTimeout)` clone; the connect is bounded separately.

`cleanupLauncher` kills the process group first and waits for `launcher.Cleanup`
only under a budget: `Cleanup` alone waits on `<-l.exit`, which never closes if
the browser was never closed, and that is what turned one failed screenshot into
a five-minute package timeout.

### Artifacts

Eight scenarios: `welcome`, `markdown`, `todo`, `dialog` at 80x24; `narrow`
(markdown at 40x12); `longoutput` at 120x40; `compressing` at 80x24 and
`compressing-narrow` at 40x12.

The page is the frame **after the terminal's own truncation**: `frameToHTML`
runs every row through `clipFrameToWidth`, which calls the same
`ansi.Truncate(line, width, "")` that bubbletea's standard renderer applies to
each line before writing it (`standard_renderer.go`, v1.3.10). A scenario builds
a model and calls `View()` directly, which skips that step, so the harness has to
apply it — the in-flight compression status line is 113 columns wide and is cut
at the geometry instead of widening the image (issue #25, fixed).

Measured after that fix: 1440 px for every 80x24 scenario, 2160 px for 120x40,
720 px for `compressing-narrow`, 732 px for `narrow` (the extra 12 px is a
fallback-font glyph whose bitmap is wider than the cell it counts as — the bound
in `capturePNG` allows the page's 24 px of horizontal padding for exactly this
reason). Below the geometry the tests do not rely on pixels:
`TestShotScenariosFitTheirGeometry` (ungated) asserts that no page row is wider
than its column count, which is what the width only witnesses.

Vertically the capture stays full-page: `dialog` at 80x24 is 28 rows and its PNG
is 1294 px tall, so no row is ever dropped.

---

## 6. Layer 3 — Real binary on a PTY (gated)

`startBinaryPTY` runs `bin/tinycode` under a real pseudo-terminal via
`creack/pty`:

- `--api-key=shot-test --base-url=http://127.0.0.1:9/v1 --model=shot-test`
  (port 9 is the discard port: no request can succeed);
- `--session-dir` inside a temp `HOME`, `--log-level=error`;
- working directory in a second temp dir, so a real `~/.tinycode/config.json`
  cannot influence the run (no MCP server or provider leaks in);
- a nil `size` uses `pty.Start` and leaves the window size unset — the `0x0`
  report a terminal that never got one gives (`TIOCGWINSZ` answers `0 0`);
- one goroutine drains the master continuously (a full PTY buffer would otherwise
  deadlock the child), and one waits on the process.

`terminalEnv` builds the child environment by **removing** and then pinning
`HOME`, `TERM`, `COLORTERM`, `NO_COLOR`, `CLICOLOR`, `CLICOLOR_FORCE` and
`FORCE_COLOR`. This matters because the harness may itself run under
`NO_COLOR=1 TERM=dumb` (an agent shell or a CI job can), and termenv honours
`NO_COLOR` by dropping to the Ascii profile — the binary would then paint a
monochrome frame and quietly turn the colour assertions into no-ops. Overriding
instead of appending matters too: a duplicate key leaves the decision to whoever
reads the environment first.

`assertBinaryFresh` fails when `bin/tinycode` is older than the newest non-test
`.go` file or `go.mod`. A stale binary makes these smoke tests lie in both
directions: passing while the tree is broken (a fix that was never built) and
failing on a bug that is already fixed. `make test-tui-visual` depends on
`build`, so it always rebuilds; `go test ./tui -run TestBinarySmoke` directly does
not.

The smoke tests:

- `TestBinarySmokeUnderPTY` — 80x24, waits for `TinyCode` in the stream, requires
  at least one real SGR sequence (`\x1b[…m`, not merely any escape), then quits.
- `TestBinarySmokeWithoutTerminalSize` — the same binary on the size-less PTY;
  the status bar is rendered outside the cell grid, so seeing `plan` proves a
  frame was produced. This is the outer-layer guard for the geometry clamp
  (`tui/geometry_test.go`): before it, the first frame panicked in `View()` and
  the process exited through `ErrProgramPanic`.
- `quit` sends the documented double `Ctrl+C` (`0x03 0x03`) and requires a clean
  exit within `shotTimeout`, otherwise it prints the stream tail.

---

## 7. Layer 4 — Live stream replay

`screenBuffer` is the smallest terminal emulator that can replay what the TUI's
renderer writes, so a screenshot can come from the live PTY stream instead of
`View()`'s return value. A regression that only lives in the renderer — a repaint
that never happens, a line that is never erased, cursor addressing one row off —
is invisible to the frame functions and shows up here.

Supported vocabulary (measured from a captured 80x24 welcome stream):

| Input | Handling |
| --- | --- |
| `\r`, `\n`, `\b`, `\t` | column/move semantics; other control bytes are ignored |
| `ESC[K`, `ESC[J` | erase line / display, modes 0, 1, 2 |
| `ESC[<n>A/B/C/D`, `ESC[<n>G`, `ESC[<n>;<m>H` | cursor movement and addressing |
| `ESC[…m` | SGR via the same `applySGR` the HTML renderer uses |
| `ESC]…BEL`/`ESC]…ST` | ignored (OSC background queries, hyperlinks) |
| other two-byte `ESC` | skipped |
| wide runes | `go-runewidth` width, with a continuation cell for the second half |

It also implements deferred wrap (a full row wraps on the *next* printable rune)
and scrolling, which the renderer relies on.

Two renderings:

- `String()` — the visible screen as plain text with trailing blanks removed; the
  same shape as a golden frame.
- `HTML()` — right-trimmed rows with `styleAttr()` spans, i.e. the styled body
  `htmlDocument` wraps for a screenshot.

Ungated fidelity tests: `TestScreenBufferReplaysViewFrame` (a rendered `View()`
frame replayed through the emulator must equal `normalizeFrame` of that frame,
and must keep its styling), plus cursor-up repaint, stale-text erase, scroll,
last-column wrap, wide runes, ignored queries and `ESC[2J`.

`TestBinaryScreenshotFromStream` (gated) starts the real binary at 80x24 and
polls the replay until every **build- and clock-independent** row of the welcome
frame (`stableBannerRows`: rows without a digit) appears verbatim — the wait *is*
the assertion, so a fixed sleep never decides whether a repaint landed. It then
requires SGR styling, requires the replayed status bar to contain `session:`, and
screenshots the replayed screen as `tinycode-pty-welcome-80x24.png`. No byte
golden of a live screen is committed: its counts, provider name and session clock
are live values.

---

## 8. Recipes

### Run one gated scenario

```bash
make build
TUI_SHOT=1 go test ./tui -run 'TestFrameScreenshots/markdown$' -v -count=1
```

Or run just the layers you need:

```bash
TUI_SHOT=1 go test ./tui -run 'TestBinarySmoke' -v -count=1     # PTY only
TUI_SHOT=1 go test ./tui -run 'TestBinaryScreenshotFromStream' -v -count=1
```

`TestFrameScreenshots` is the only gated test that needs Chromium but not the
binary; the PTY tests are the reverse.

### Add a scenario

1. Add a builder to `frame_golden_test.go` (mirror an existing one; start from
   `frameModel`, and keep time- or config-dependent values out of the frame).
2. Append `{name, []frameSize{…}, frameFoo}` to `frameScenarios`.
3. `go test ./tui -run Golden -update`, then **read the new frame files** before
   committing them.
4. If the scenario also deserves an image, append it to `shotScenarios` in
   `frame_shot_test.go` (reuse the same builder — a golden and a PNG must come
   from one source of truth) and run `make test-tui-visual`.
5. Open the PNG (or `read_image` it, for an agent) and confirm it shows what the
   name claims.

### Intentional layout change

```bash
go test ./tui -run Golden -update      # rewrite the 28 golden files
git diff --stat tui/testdata/golden/   # then inspect every changed frame
make test-tui-visual                   # and look at the affected PNGs
```

Golden changes belong in the same commit as the code change that caused them,
with the new frames' content reviewed — `-update` will happily record a
regression.

### Capture a one-off frame without gating the suite

Set the output directory and run only the screenshot test:

```bash
TUI_SHOT=1 TUI_SHOT_DIR=/tmp/tui-shots go test ./tui -run TestFrameScreenshots -count=1
open /tmp/tui-shots/tinycode-frame-markdown-80x24.png
```

### Reproduce a wedge locally

`shotTimeout` is a variable precisely so a gated run can be starved on demand:

```go
shotTimeout = 1 * time.Second   // reproduces a loaded-runner hang in seconds
shotTimeout = 1 * time.Millisecond // proves the failure is clean and attributed
```

With 1 ms the failure reads `connect to ws://…: no result within 1ms (a wedged CDP
call; the stage goroutine stays blocked)` — fast, named and without a five-minute
package timeout. A gated harness that cannot be starved on demand can only be
debugged on CI.

### Prove a fix with mutations

Before trusting a new assertion, break the code it is supposed to catch (make the
`/compress` command run inline, swallow the summarizer error, drop the Ctrl+C
branch) and confirm the named test fails; then confirm the tracked diff hash is
identical after restoring. An assertion that cannot fail is not evidence.

---

## 9. Environment variables and knobs

| Name | Default | Meaning |
| --- | --- | --- |
| `TUI_SHOT` | unset | Gate for every browser/PTY test; `1` runs them, unset skips with the reason |
| `TUI_SHOT_DIR` | `/tmp` | Where PNGs, the intermediate `.html` and the throwaway browser profile go |
| `CHROME_PATH`, `CHROME` | unset | Highest-priority browser overrides, probed before the `PATH` and Playwright candidates |
| `HOME` | inherited | Also determines the Playwright cache location; redirecting it (an agent sandbox does) hides the cache from discovery |
| `NO_COLOR`, `TERM`, `COLORTERM`, `CLICOLOR*`, `FORCE_COLOR` | inherited | Removed and re-pinned for the PTY child by `terminalEnv` |
| `shotTimeout` (Go variable, not env) | 20 s | Budget for one stage, the connect, one PTY poll and the cleanup wait |
| `browserProbeTimeout` (Go variable) | 5 s | `--version` probe budget during browser discovery |
| `browserProbeRetryAfter` (Go variable) | 30 s | How long a probe that *ran out of budget* is believed; a positive or definitive negative verdict is kept for the process lifetime |

`-update` is a test flag, not an environment variable:
`go test ./tui -run Golden -update`.

### Sandbox and CI notes

- **Agent sandbox (this repo's `AGENTS.md`, "Local Verification"):** the suite
  needs `HOME` and `GIT_CONFIG_GLOBAL` redirected, plus redirected Go caches.
  If `HOME` is redirected, set `CHROME_PATH`, or discovery looks in the wrong
  Playwright cache and the visual harness skips.
- **`/dev/ptmx`:** the PTY tests need a terminal device. The default
  `workspace-write` sandbox denies it, so run the PTY half with
  `sandbox_permissions: danger-full-access` (or on a normal host/in CI).
- **Chromium in a sandbox:** the launcher passes `--no-sandbox`, but a sandbox
  that denies the profile directory will still fail; `TUI_SHOT_DIR` must be
  writable.
- **CI:** the ungated golden frames, the screen-buffer tests and the two bounds
  tests run in the `ci` job (no browser, no terminal). The `tui-visual` job
  installs Chrome for Testing via `browser-actions/setup-chrome@v1` and runs
  `make test-tui-visual`. Annotation baseline is *not* zero: one
  `ubuntu-latest` migration notice per job, plus the `setup-chrome@v1` Node 20
  deprecation warning on the two browser jobs. Gate on no *new* annotations.

---

## 10. Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| `frame … carries no SGR sequences` | The colour profile was lost (non-TTY stdout) | Keep the render inside `withTrueColor`; do not cache styles across a profile change |
| Golden mismatch right after a change | Correct, if the layout change was intended | Review `frameDiff`, then `go test ./tui -run Golden -update` |
| `read golden …: no such file` | A new scenario has no baseline yet | Regenerate with `-update`, and commit the new files |
| Screenshot test skips with "no Chromium/Chrome installed" | Discovery found no usable binary | Install Chromium, or set `CHROME_PATH`; if `HOME` was redirected, set it explicitly |
| `bin/tinycode is older than …: run make build first` | Stale binary | `make build`, or use `make test-tui-visual` |
| PTY tests fail to start | No `/dev/ptmx` (sandbox) | Widen the sandbox for that command, or run in CI |
| PNG is a few hundred bytes | The page rendered empty | Inspect the `.html` next to it; check that the frame still has content at that geometry |
| `no result within 20s (a wedged CDP call…)` | A stage is blocked inside rod | Read the named stage; lower `shotTimeout` to reproduce, then check the browser profile and version |
| The whole package times out at 5m | A stage or cleanup is unbounded (the pre-#28 failure) | Ensure the stage goes through `runStage` and the launcher through `cleanupLauncher` |
| `make test-tui-visual` passes locally, fails on CI | Runner load vs. a shared deadline | Every stage must take a fresh `browser.Timeout(shotTimeout)`; never install one at connect |

---

## 11. Design rules

The rules behind the code, each with the incident it came from:

1. **One budget per stage, named in the failure.** `runStage` bounds each stage
   and the caller labels it. Without it a wedged CDP call reports nothing until
   `go test -timeout` kills the package, and the goroutine dump is the only
   evidence (issue #20).
2. **Return errors, never panic, inside a stage.** A panic in a subtest goroutine
   skips deferred cleanup; rod's `Must*` helpers did exactly that and left
   `launcher.Cleanup` waiting forever (issue #20).
3. **Cleanup kills before it waits**, and bounds the wait.
4. **A deadline belongs to one operation, not to a client.** `rod.Browser.Timeout`
   installs one expiring context that every derived page inherits; installed at
   connect it covered all eight screenshots and expired mid-run on a slow runner.
   The Ollama provider (#1) is the same shape with no bound at all.
5. **Timing knobs are injectable.** `shotTimeout` shrinking to 1 s reproduced the
   CI hang locally; 1 ms proved the failure is clean and attributed.
6. **A golden frame is the assertion; a PNG supports it.** A screenshot is
   evidence only when its dimensions are pinned to the geometry it claims to
   show: a full-page capture of an unclipped frame widens to its longest line
   (the compression scenario used to be 1912 px wide at *both* 80x24 and 40x12,
   because its status bar is 113 columns long — issue #25), so a scenario's golden
   and PNG come from one builder, the page is clipped with the renderer's own
   truncation, and the artifact's size is asserted.

The short form of these rules lives in the repo's `AGENTS.md`; this document is
the longer design record.

---

## 12. Known limits and open issues

- **Full-page capture (#25, fixed).** PNGs are still full-page vertically —
  `dialog` at 80x24 is 28 rows and keeps all of them — but every row
  is now clipped at the geometry with the renderer's own `ansi.Truncate`, so the
  page is no longer widened by a status bar longer than the terminal. The width
  can still differ by a few pixels from `W*9` when a row uses a fallback-font
  glyph, which is why the assertion is a bound and the column count itself is
  asserted in `TestShotScenariosFitTheirGeometry` instead of on pixels.
- **Renderer truncation is not CellGrid clipping.** The message area is clipped by
  `CellGrid` (wide-rune aware, at `m.width`); the status bar is built by
  `fmt.Sprintf` with no bound and is only cut by bubbletea's standard renderer
  just before it writes the line. A frame captured from `View()` therefore
  contains columns no terminal ever received, which is exactly what
  `clipFrameToWidth` reproduces.
- **No live-screen byte golden.** A live frame carries counts, the provider name
  and the session clock, so only the build- and clock-independent rows are
  compared and the rest is asserted structurally (styling present, status bar
  present).
- **Browser discovery is load-sensitive (#26, fixed).** The `--version` probe is
  bounded by `browserProbeTimeout`, and a probe that the budget ends is remembered
  only for `browserProbeRetryAfter` (30 s) instead of for the process lifetime, so
  one loaded moment no longer disables the browser for every later extraction. The
  fixture that made the test flaky is a native binary now, not a shell script.
- **`screenBuffer` is not a full terminal.** It reproduces cell content and
  styling; it ignores scroll regions, alternate screens, tabs beyond 8 columns
  and every mode change. That is deliberate — a renderer change that needs more
  fails loudly instead of silently producing garbage.
- **The visual layers are advisory gates.** They run in the `tui-visual` job, are
  skipped locally without `TUI_SHOT=1`, and no assertion replaces looking at the
  PNG for a change whose whole point is how it looks.

---

## 13. File map

| Symbol | File | Purpose |
| --- | --- | --- |
| `withTrueColor`, `resetStyleCache` | `tui/frame_golden_test.go` | Pin the TrueColor profile for one render |
| `normalizeFrame`, `frameDiff`, `assertGolden` | `tui/frame_golden_test.go` | Text form, readable diff, golden read/write |
| `frameModel`, `frame*`, `frameScenarios` | `tui/frame_golden_test.go` | Scenario builders and geometry tiers |
| `TestGoldenFrames`, `TestGoldenFrameANSI`, `TestFrameScenariosRenderTwice` | `tui/frame_golden_test.go` | The always-on layer |
| `ansiState`, `ansiPalette`, `xterm256ToCSS`, `applySGR`, `frameToHTML`, `htmlDocument` | `tui/frame_shot_test.go` | SGR → CSS → page |
| `runStage`, `shotTimeout`, `cleanupLauncher` | `tui/frame_shot_test.go` | Bounds |
| `newShotLauncher`, `connectBrowser`, `capturePNG`, `shotDir` | `tui/frame_shot_test.go` | Chromium lifecycle and capture |
| `startBinaryPTY`, `terminalEnv`, `assertBinaryFresh`, `ptySmoke.quit`, `waitForStream` | `tui/frame_shot_test.go` | Real binary on a PTY |
| `screenBuffer` (+ `Write`, `String`, `HTML`, `csi`, `parseInts`) | `tui/pty_screen_test.go` | Terminal emulator |
| `replayScreen`, `stableBannerRows`, `missingRows` | `tui/pty_screen_test.go` | Replay helpers |
| `stripANSIView` | `tui/view_integration_test.go` | CSI stripper shared with the other view tests |
| `lockedBuffer` | `tui/program_driver_test.go` | Read-while-writing output buffer |
| `FindBrowser` | `tool/web_browser.go` | Chromium discovery, shared with web extraction |
| `test-tui-visual` | `Makefile` | Builds the binary and runs the gated tests |

### How the counts in this document are measured

```bash
ls tui/testdata/golden/frames/*.txt | wc -l    # 27
ls tui/testdata/golden/ansi/*.ansi | wc -l     # 1
grep -c '^\t{"' tui/frame_shot_test.go         # 8 shot scenarios
grep -c '^func TestBinary' tui/frame_shot_test.go tui/pty_screen_test.go  # 2 + 1
```
