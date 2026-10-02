package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/TinyCode/tuiprobe/golden"
	tuiprobepty "github.com/yusiwen/TinyCode/tuiprobe/pty"
	tuifont "github.com/yusiwen/TinyCode/tuiprobe/render/font"
	"github.com/yusiwen/TinyCode/tuiprobe/screen"
	"github.com/yusiwen/TinyCode/tuiprobe/session"
)

// This file is the parity run: the same scenarios, rendered by TinyCode's own
// builders, asserted through tuiprobe against the goldens the existing harness
// committed.
//
// The text half is deliberately merciless — the tool must reproduce the committed
// files **byte for byte**. It is the strongest statement available here: the same
// frames, the same expectations, and no room for "close enough". If tuiprobe's
// normalization, geometry check or emulator disagreed with the harness in any way,
// these tests would show it as a diff instead of an opinion.
//
// The gated half (TUI_SHOT=1) mirrors the harness's own gated checks: PNGs of the
// same scenarios, and the real binary on a PTY. It runs in the same CI job as the
// harness, which is what makes this a parallel run rather than a replacement.

// tuiprobeGoldenDir is where the harness keeps its committed artifacts.
const tuiprobeGoldenDir = "testdata/golden"

// TestParityGoldenFramesWithTuiprobe renders every golden scenario with TinyCode's
// builders and requires the tuiprobe-produced text to equal the committed file.
func TestParityGoldenFramesWithTuiprobe(t *testing.T) {
	frames, overlong := 0, 0
	for _, sc := range frameScenarios {
		for _, size := range sc.sizes {
			name := fmt.Sprintf("%s_%s", sc.name, size)
			t.Run(name, func(t *testing.T) {
				var ansi string
				withTrueColor(t, func() {
					m := sc.build(size.W, size.H)
					ansi = m.View()
				})

				// The mechanism guard the harness has: a frame without a single SGR
				// sequence means the colour profile was lost, and the comparison
				// would then be about stripped text.
				if !strings.Contains(ansi, "\x1b[") {
					t.Fatalf("frame %s carries no SGR sequences: the color profile was not pinned", name)
				}

				// Geometry, exactly as the harness asserts it: the *clipped* frame
				// must fit, the clip must be idempotent, and at least one scenario
				// must be overlong before clipping or the clip is untested. A frame
				// is wider than its terminal on purpose here — the status bar is
				// drawn outside the cell grid.
				clipped := golden.Clip(size.W, ansi)
				// Width only, as the harness does it: a frame may be taller than the
				// terminal, which clips or scrolls it, but a row wider than the
				// terminal is cut off mid-glyph.
				if err := golden.FitsWidth(size.W, clipped); err != nil {
					t.Errorf("the clipped frame does not fit: %v", err)
				}
				if again := golden.Clip(size.W, clipped); again != clipped {
					t.Error("the clip is not idempotent")
				}
				if err := golden.FitsWidth(size.W, ansi); err != nil {
					overlong++
				}

				// The assertion: byte for byte against the harness's own golden.
				if err := golden.Compare(filepath.Join(tuiprobeGoldenDir, "frames"), name+".txt", golden.Normalize(ansi)); err != nil {
					t.Errorf("%v", err)
					return
				}
				frames++
			})
		}
	}
	t.Logf("tuiprobe reproduced %d committed text goldens byte for byte", frames)
	if overlong == 0 {
		t.Error("no scenario is wider than its geometry any more, so the clip is untested")
	}
	t.Logf("%d of %d scenario renderings are wider than their terminal before clipping (the status bar)", overlong, frames)
}

// TestParityANSIGoldenWithTuiprobe does the same for the one frame kept with its
// escape sequences: colour, bold, underline and the OSC 8 link must be identical.
func TestParityANSIGoldenWithTuiprobe(t *testing.T) {
	var ansi string
	withTrueColor(t, func() {
		ansi = frameMarkdown(80, 24).View()
	})

	if !strings.Contains(ansi, "\x1b[") {
		t.Fatal("the ANSI frame carries no escapes: the colour profile was not pinned")
	}
	if err := golden.Compare(filepath.Join(tuiprobeGoldenDir, "ansi"), "markdown_80x24.ansi", ansi); err != nil {
		t.Errorf("%v", err)
	}
}

// TestParityDeterminismWithTuiprobe asks tuiprobe's determinism helper the same
// question the harness asks: render twice, and be identical.
func TestParityDeterminismWithTuiprobe(t *testing.T) {
	for _, sc := range frameScenarios {
		for _, size := range sc.sizes {
			name := fmt.Sprintf("%s_%s", sc.name, size)
			t.Run(name, func(t *testing.T) {
				err := golden.Deterministic(size.W, size.H, func(w, h int) string {
					var frame string
					withTrueColor(t, func() { frame = sc.build(w, h).View() })
					return frame
				})
				if err != nil {
					t.Errorf("%v", err)
				}
			})
		}
	}
}

// TestParityScreenshotsWithTuiprobe renders the same scenarios the harness
// screenshots, with the tool's font renderer instead of Chromium, and pins each
// image's pixel size to the geometry. The artifact *set* and its geometry are what
// parity means for images; the pixels themselves come from different renderers.
func TestParityScreenshotsWithTuiprobe(t *testing.T) {
	requireShot(t)
	dir := shotDir(t)
	renderer, err := tuifont.New(tuifont.Options{Scale: 1})
	if err != nil {
		t.Fatalf("font renderer: %v", err)
	}
	cellW, cellH := renderer.CellSize()

	for _, sc := range shotScenarios {
		t.Run(sc.name, func(t *testing.T) {
			var frame string
			withTrueColor(t, func() { frame = sc.build(sc.size.W, sc.size.H).View() })

			// Feed the frame through tuiprobe's emulator and draw the screen: the
			// same path a live session takes.
			terminal := screen.New(sc.size.W, sc.size.H)
			if _, err := terminal.Write([]byte(strings.ReplaceAll(frame, "\n", "\r\n"))); err != nil {
				t.Fatalf("feed the emulator: %v", err)
			}

			path := filepath.Join(dir, fmt.Sprintf("parity-%s-%s.png", sc.name, sc.size))
			file, err := os.Create(path)
			if err != nil {
				t.Fatalf("create %s: %v", path, err)
			}
			defer file.Close()
			width, height, err := renderer.EncodePNG(file, terminal)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if wantW, wantH := sc.size.W*cellW, sc.size.H*cellH; width != wantW || height != wantH {
				t.Errorf("%s is %dx%d, want %dx%d for a %s screen", path, width, height, wantW, wantH, sc.size)
			}
			t.Logf("%s: %dx%d", path, width, height)
		})
	}
}

// TestParityBinarySmokeWithTuiprobe drives the real binary on a PTY through
// tuiprobe's session and asserts what the harness asserts: the startup stream
// carries styling, and the documented double Ctrl+C quits.
func TestParityBinarySmokeWithTuiprobe(t *testing.T) {
	requireShot(t)
	argv, env, dir := parityBinaryLaunch(t)

	sess, err := session.Start(session.Options{
		Args: argv,
		Dir:  dir,
		Env:  env,
		Size: tuiprobepty.Size{Cols: 80, Rows: 24},
	})
	if err != nil {
		t.Fatalf("start the binary through tuiprobe: %v", err)
	}
	defer sess.Close()

	if err := sess.WaitText("TinyCode", 30*time.Second); err != nil {
		t.Fatalf("%v\ntrace tail:\n%s", err, tail(sess.Trace(2000), 800))
	}
	if !sgrSequence.MatchString(sess.Trace(0)) {
		t.Error("the startup stream carried no SGR styling: termenv did not see the PTY as a colour terminal")
	}

	// The documented way out: Ctrl+C twice.
	if err := sess.Send("ctrl+c", "ctrl+c"); err != nil {
		t.Fatalf("send: %v", err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = sess.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the binary did not quit on a double Ctrl+C within 30s")
	}
}

// TestParityBinaryAlwaysGetsASizeWithTuiprobe is the harness's size-less PTY case
// expressed in the tool's terms: tuiprobe never hands a program a 0x0 terminal, so
// the crash the harness guards against cannot be reached through it.
func TestParityBinaryAlwaysGetsASizeWithTuiprobe(t *testing.T) {
	requireShot(t)
	argv, env, dir := parityBinaryLaunch(t)

	sess, err := session.Start(session.Options{
		Args: argv,
		Dir:  dir,
		Env:  env,
		Size: tuiprobepty.Size{}, // the caller asks for nothing
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sess.Close()

	if size := sess.Size(); size.Cols <= 0 || size.Rows <= 0 {
		t.Fatalf("the session was given %dx%d; the tool must upgrade a zero size", size.Cols, size.Rows)
	}
	// "plan" is the status bar's mode label: it is painted outside the cell grid, so
	// seeing it proves a frame was produced rather than that a panic happened.
	if err := sess.WaitText("plan", 30*time.Second); err != nil {
		t.Fatalf("%v\ntrace tail:\n%s", err, tail(sess.Trace(2000), 800))
	}
	if strings.Contains(sess.Text(), "panic:") {
		t.Fatalf("the size-less run panicked:\n%s", sess.Text())
	}
}

// TestParityStreamScreenshotWithTuiprobe takes the screenshot from the *live* stream
// rather than from a frame function: the harness's #16 layer, through the tool.
func TestParityStreamScreenshotWithTuiprobe(t *testing.T) {
	requireShot(t)
	argv, env, work := parityBinaryLaunch(t)
	dir := shotDir(t)
	renderer, err := tuifont.New(tuifont.Options{Scale: 1})
	if err != nil {
		t.Fatal(err)
	}
	cellW, cellH := renderer.CellSize()

	sess, err := session.Start(session.Options{
		Args: argv,
		Dir:  work,
		Env:  env,
		Size: tuiprobepty.Size{Cols: 80, Rows: 24},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sess.Close()

	if err := sess.WaitStable(300*time.Millisecond, 30*time.Second); err != nil {
		t.Fatalf("%v", err)
	}
	// Clear the screen the emulator holds, then replay the raw stream into it: this
	// is the stream the program really wrote, not a re-render.
	terminal := screen.New(80, 24)
	if _, err := terminal.Write([]byte(sess.Trace(0))); err != nil {
		t.Fatalf("replay the stream: %v", err)
	}

	path := filepath.Join(dir, "parity-live-stream-80x24.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	width, height, err := renderer.EncodePNG(file, terminal)
	if err != nil {
		t.Fatalf("render the replayed stream: %v", err)
	}
	if wantW, wantH := 80*cellW, 24*cellH; width != wantW || height != wantH {
		t.Errorf("%s is %dx%d, want %dx%d", path, width, height, wantW, wantH)
	}
	t.Logf("%s: %dx%d from the live stream", path, width, height)
}

// parityBinaryLaunch is how the harness starts the binary for its smoke tests:
// an unreachable provider, a throwaway session directory, quiet logging, and a
// hermetic HOME and working directory. Without these the program waits for a
// provider or a first-run answer and paints nothing, which is exactly what the
// first version of this parity test observed.
func parityBinaryLaunch(t *testing.T) (argv []string, env []string, dir string) {
	t.Helper()
	binary := parityBinary(t)
	home := t.TempDir()
	return []string{
			binary,
			"--api-key=shot-test",
			"--base-url=http://127.0.0.1:9/v1",
			"--model=shot-test",
			"--session-dir=" + filepath.Join(home, "sessions"),
			"--log-level=error",
		},
		// The tool's own hermetic-terminal helper, rather than the harness's: the
		// agent shell this runs in has TERM=dumb and NO_COLOR=1, and inheriting
		// those would make the program paint plain text and fail the SGR assertion
		// for a reason that has nothing to do with the tool.
		tuiprobepty.TerminalEnv(home),
		t.TempDir()
}

// parityBinary resolves bin/tinycode the way the harness does, and skips with the
// same reason when it is missing.
func parityBinary(t *testing.T) string {
	t.Helper()
	binary, err := filepath.Abs(filepath.Join("..", "bin", "tinycode"))
	if err != nil {
		t.Fatalf("resolve binary path: %v", err)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Skipf("%s is missing: run `make build` first (make test-tui-visual does)", binary)
	}
	assertBinaryFresh(t, binary, info)
	return binary
}
