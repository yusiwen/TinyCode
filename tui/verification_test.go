package tui

// The TUI verification suite: the project's fixtures (scenarios_test.go) asserted
// through tuiprobe.
//
// This file is deliberately thin. It renders a scenario, hands the string or the
// bytes to the tool, and reports what the tool says:
//
//   - frames (goldens, the ANSI golden, determinism, geometry)  tuiprobe/golden
//   - images                                                     tuiprobe/render/font
//   - the real binary on a real PTY                              tuiprobe/session
//
// The gated half runs with `TUI_SHOT=1 make test-tui-visual`; without it those tests
// skip with the reason, so a plain `make test` never needs a browser, a PTY, or a
// built binary.
//
// Update intended layout changes with the tool's own flag:
//
//	go test ./tui -run Golden -update

import (
	"flag"
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

// goldenDir is where the committed frames live, under testdata so `go test` can read
// them and the tool can rewrite them with -update.
const goldenDir = "testdata/golden"

// TestGoldenFrames pins every scenario's text frame.
//
// The frame must fit the terminal it claims (after clipping: a frame may be taller
// and wider than its terminal, because the status bar is drawn outside the cell grid
// — issue #25), it must carry styling, and it must match the committed file byte for
// byte.
func TestGoldenFrames(t *testing.T) {
	overlong := 0
	for _, sc := range frameScenarios {
		for _, size := range sc.sizes {
			t.Run(fmt.Sprintf("%s_%s", sc.name, size), func(t *testing.T) {
				var ansi string
				withTrueColor(t, func() { ansi = sc.build(size.W, size.H).View() })

				// A frame without a single escape sequence means the colour profile was
				// lost, not that the frame is "plain": guard the mechanism so a silent
				// regression cannot pass.
				if !strings.Contains(ansi, "\x1b[") {
					t.Fatalf("frame %s carries no SGR sequences: the color profile was not pinned", size)
				}

				clipped := golden.Clip(size.W, ansi)
				if err := golden.FitsWidth(size.W, clipped); err != nil {
					t.Errorf("the clipped frame does not fit: %v", err)
				}
				if again := golden.Clip(size.W, clipped); again != clipped {
					t.Error("the clip is not idempotent")
				}
				if err := golden.FitsWidth(size.W, ansi); err != nil {
					overlong++
				}

				// Assert reports the first differing line itself, and rewrites the file
				// under the tool's -update flag: the same habit as before.
				golden.Assert(t, filepath.Join(goldenDir, "frames"),
					fmt.Sprintf("%s_%s.txt", sc.name, size), golden.Normalize(ansi))
			})
		}
	}
	// If nothing is wider than its terminal any more, the clip above is untested.
	if overlong == 0 {
		t.Error("no scenario is wider than its geometry any more, so the clip is untested")
	}
}

// TestGoldenFrameANSI keeps one full ANSI frame under version control, so the exact
// escape sequences (colour, bold, underline, OSC 8 links) are diffable and not only
// their stripped text.
func TestGoldenFrameANSI(t *testing.T) {
	var ansi string
	withTrueColor(t, func() { ansi = frameMarkdown(80, 24).View() })

	if !strings.Contains(ansi, "\x1b[") {
		t.Fatal("the ANSI frame carries no escapes: the colour profile was not pinned")
	}
	golden.Assert(t, filepath.Join(goldenDir, "ansi"), "markdown_80x24.ansi", ansi)
}

// TestFrameScenariosRenderTwice proves the incremental render path is stable: a
// second View() on the same model must return the identical frame. A diff here means
// the grid's dirty tracking leaks state between frames.
func TestFrameScenariosRenderTwice(t *testing.T) {
	for _, sc := range frameScenarios {
		for _, size := range sc.sizes {
			t.Run(fmt.Sprintf("%s_%s", sc.name, size), func(t *testing.T) {
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

// TestShotScenariosFitTheirGeometry checks the images against the geometry they
// claim: each row, clipped the way the renderer clips it, must fit the terminal's
// width, and the clip must be idempotent.
func TestShotScenariosFitTheirGeometry(t *testing.T) {
	overlong := 0
	for _, sc := range frameScenarios {
		if len(sc.shots) == 0 {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			for _, shot := range sc.shots {
				var frame string
				withTrueColor(t, func() { frame = sc.build(shot.size.W, shot.size.H).View() })

				clipped := golden.Clip(shot.size.W, frame)
				if err := golden.FitsWidth(shot.size.W, clipped); err != nil {
					t.Errorf("%s: the clipped frame does not fit: %v", shot.name, err)
				}
				if again := golden.Clip(shot.size.W, clipped); again != clipped {
					t.Errorf("%s: the clip is not idempotent", shot.name)
				}
				if err := golden.FitsWidth(shot.size.W, frame); err != nil {
					overlong++
				}
			}
		})
	}
	if overlong == 0 {
		t.Error("no scenario is wider than its geometry any more, so the clip is untested")
	}
}

// TestFrameScreenshots renders each scenario to a PNG with the tool's font renderer
// and pins every image's pixel size to the geometry it claims to show. The text
// golden is the assertion; the image is what a person looks at.
func TestFrameScreenshots(t *testing.T) {
	requireShot(t)
	dir := shotOutputDir(t)

	renderer, err := tuifont.New(tuifont.Options{Scale: 1})
	if err != nil {
		t.Fatalf("font renderer: %v", err)
	}
	cellW, cellH := renderer.CellSize()

	for _, sc := range frameScenarios {
		for _, shot := range sc.shots {
			t.Run(shot.name, func(t *testing.T) {
				var frame string
				withTrueColor(t, func() { frame = sc.build(shot.size.W, shot.size.H).View() })

				// The screen is what the terminal would show: the frame goes through
				// the same emulator a live session uses.
				terminal := screen.New(shot.size.W, shot.size.H)
				if _, err := terminal.Write([]byte(strings.ReplaceAll(frame, "\n", "\r\n"))); err != nil {
					t.Fatalf("feed the emulator: %v", err)
				}

				path := filepath.Join(dir, fmt.Sprintf("tinycode-%s-%s.png", shot.name, shot.size))
				file, err := os.Create(path)
				if err != nil {
					t.Fatalf("create %s: %v", path, err)
				}
				defer file.Close()

				width, height, err := renderer.EncodePNG(file, terminal)
				if err != nil {
					t.Fatalf("render %s: %v", path, err)
				}
				if wantW, wantH := shot.size.W*cellW, shot.size.H*cellH; width != wantW || height != wantH {
					t.Errorf("%s is %dx%d, want %dx%d for a %s screen", path, width, height, wantW, wantH, shot.size)
				}
				t.Logf("%s: %dx%d", path, width, height)
			})
		}
	}
}

// TestBinarySmokeUnderPTY starts the built binary on a real PTY at 80x24, waits for
// the startup frame, asserts the stream carried styling, and quits with the
// documented double Ctrl+C. This is the only check that exercises main.go's wiring,
// the real renderer and a real terminal.
func TestBinarySmokeUnderPTY(t *testing.T) {
	requireShot(t)
	argv, env, dir := binaryLaunch(t)

	sess, err := session.Start(session.Options{
		Args: argv,
		Dir:  dir,
		Env:  env,
		Size: tuiprobepty.Size{Cols: 80, Rows: 24},
	})
	if err != nil {
		t.Fatalf("start the binary on a PTY: %v", err)
	}
	defer sess.Close()

	if err := sess.WaitText("TinyCode", 30*time.Second); err != nil {
		t.Fatalf("%v\ntrace tail:\n%s", err, traceTail(sess))
	}
	if !strings.Contains(sess.Trace(0), "\x1b[") {
		t.Error("the startup stream carried no SGR styling: termenv did not see the PTY as a colour terminal")
	}

	// The documented way out: Ctrl+C twice.
	if err := sess.Send("ctrl+c", "ctrl+c"); err != nil {
		t.Fatalf("send Ctrl+C: %v", err)
	}
	exited := make(chan int, 1)
	go func() {
		code, _ := sess.Wait()
		exited <- code
	}()
	select {
	case code := <-exited:
		if code != 0 {
			t.Fatalf("the binary exited with code %d\nstream tail:\n%s", code, traceTail(sess))
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("the binary did not exit after Ctrl+C twice\nstream tail:\n%s", traceTail(sess))
	}
}

// TestBinarySmokeWithoutTerminalSize runs the binary on a PTY that was never given a
// window size: the kernel reports 0x0, and the tool upgrades it. Before that upgrade
// the first frame panicked in View() and the process exited through ErrProgramPanic
// instead of painting.
func TestBinarySmokeWithoutTerminalSize(t *testing.T) {
	requireShot(t)
	argv, env, dir := binaryLaunch(t)

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
		t.Fatalf("the session was given %dx%d; a zero size must be upgraded", size.Cols, size.Rows)
	}
	// The status bar is rendered outside the cell grid, so it survives a one-cell-wide
	// layout and proves a frame was produced.
	if err := sess.WaitText("plan", 30*time.Second); err != nil {
		t.Fatalf("%v\ntrace tail:\n%s", err, traceTail(sess))
	}
	if strings.Contains(sess.Text(), "panic:") {
		t.Fatalf("the size-less run panicked\nscreen:\n%s", sess.Text())
	}
}

// TestBinaryScreenshotFromStream renders the *live* stream rather than a frame
// function: what the program actually painted, replayed through the emulator.
func TestBinaryScreenshotFromStream(t *testing.T) {
	requireShot(t)
	argv, env, dir := binaryLaunch(t)

	renderer, err := tuifont.New(tuifont.Options{Scale: 1})
	if err != nil {
		t.Fatalf("font renderer: %v", err)
	}
	cellW, cellH := renderer.CellSize()

	sess, err := session.Start(session.Options{
		Args: argv,
		Dir:  dir,
		Env:  env,
		Size: tuiprobepty.Size{Cols: 80, Rows: 24},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer sess.Close()

	if err := sess.WaitStable(300*time.Millisecond, 30*time.Second); err != nil {
		t.Fatalf("%v\ntrace tail:\n%s", err, traceTail(sess))
	}

	terminal := screen.New(80, 24)
	if _, err := terminal.Write([]byte(sess.Trace(0))); err != nil {
		t.Fatalf("replay the stream: %v", err)
	}
	path := filepath.Join(shotOutputDir(t), "tinycode-pty-stream-80x24.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
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

// requireShot skips a gated test with its reason, so a plain `make test` reports it
// as skipped rather than silently not running.
func requireShot(t *testing.T) {
	t.Helper()
	if os.Getenv("TUI_SHOT") == "" {
		t.Skip("TUI_SHOT is not set: run `make test-tui-visual` (needs bin/tinycode and a real PTY)")
	}
}

// shotOutputDir is where images are written: TUI_SHOT_DIR, or the temporary
// directory when it is unset.
func shotOutputDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("TUI_SHOT_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	return dir
}

// binaryLaunch starts the built binary the way the smoke tests need it: an
// unreachable provider, a throwaway session directory, quiet logging, and a hermetic
// HOME and working directory. Without these the program waits for a provider or a
// first-run answer and paints nothing.
func binaryLaunch(t *testing.T) (argv, env []string, dir string) {
	t.Helper()
	binary := binaryPath(t)
	home := t.TempDir()
	return []string{
			binary,
			"--api-key=shot-test",
			"--base-url=http://127.0.0.1:9/v1",
			"--model=shot-test",
			"--session-dir=" + filepath.Join(home, "sessions"),
			"--log-level=error",
		},
		// The tool's hermetic terminal environment: this process may be running under
		// an agent shell with TERM=dumb and NO_COLOR=1, and inheriting those would make
		// the program paint plain text.
		tuiprobepty.TerminalEnv(home),
		t.TempDir()
}

// binaryPath resolves bin/tinycode and refuses a stale one: a binary older than the
// sources it embeds verifies the previous revision, which is worse than not running.
func binaryPath(t *testing.T) string {
	t.Helper()
	binary, err := filepath.Abs(filepath.Join("..", "bin", "tinycode"))
	if err != nil {
		t.Fatalf("resolve binary path: %v", err)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Skipf("%s is missing: run `make build` first (make test-tui-visual does)", binary)
	}
	if newest, name := newestSource(t, ".."); !newest.IsZero() && info.ModTime().Before(newest) {
		t.Fatalf("%s is older than %s (%s): run `make build` first",
			binary, name, newest.Format(time.RFC3339))
	}
	return binary
}

// newestSource is the modification time of the newest .go file under root, ignoring
// the directories that cannot affect the binary.
func newestSource(t *testing.T, root string) (time.Time, string) {
	t.Helper()
	var newest time.Time
	var name string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // a directory we cannot read cannot be newer
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "bin", "dist", "testdata", "node_modules", "tuiprobe":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().After(newest) {
			newest, name = info.ModTime(), path
		}
		return nil
	})
	if err != nil {
		return time.Time{}, ""
	}
	return newest, name
}

// traceTail is the recent output of a session, for a failure message.
func traceTail(sess *session.Session) string {
	return tail(sess.Trace(0), 800)
}

// TestGoldenUpdateFlagIsRegistered pins the documented workflow. A flag that is not
// registered at parse time makes `go test ./tui -run Golden -update` fail before a
// single test runs, which is indistinguishable from "nothing needed updating" when the
// output is not read — and that is exactly how it presented (issue #85).
func TestGoldenUpdateFlagIsRegistered(t *testing.T) {
	if flag.Lookup("update") == nil {
		t.Fatal(`the -update flag is not registered: "go test ./tui -run Golden -update" cannot work`)
	}
}
