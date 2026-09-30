package tui

import (
	"bytes"
	"fmt"
	"html"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/yusiwen/tinycode/tool"
)

// On-demand visual checks. Both tests are skipped unless TUI_SHOT=1, so
// `make test` never launches a browser and never needs a built binary:
//
//	TUI_SHOT=1 make test-tui-visual
//
// TestFrameScreenshots turns the committed frame scenarios into PNGs, which is
// what lets an agent (or a reviewer) look at the layout instead of reading the
// goldens. TestBinarySmokeUnderPTY runs the real binary in a real PTY and
// proves the end-to-end path still paints and quits.

// shotEnv gates the visual harness.
const shotEnv = "TUI_SHOT"

// shotTimeout bounds every wait in the visual harness.
const shotTimeout = 20 * time.Second

func requireShot(t *testing.T) {
	t.Helper()
	if os.Getenv(shotEnv) == "" {
		t.Skipf("set %s=1 to run the visual harness (make test-tui-visual)", shotEnv)
	}
}

// shotDir returns where the PNGs are written: TUI_SHOT_DIR when set, otherwise
// /tmp so the artifacts are easy to hand to a reviewer, falling back to the
// platform temp dir.
func shotDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("TUI_SHOT_DIR"); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
		return dir
	}
	if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
		return "/tmp"
	}
	return t.TempDir()
}

// --- ANSI frame -> HTML ---------------------------------------------------

// ansiState is the subset of SGR state the frame can carry.
type ansiState struct {
	bold, dim, italic, underline bool
	fg, bg                       string
}

// ansiPalette maps the 16 base colours to CSS.
var ansiPalette = []string{
	"#1c1c1c", "#d75f5f", "#87af5f", "#d7af5f", "#5f87d7", "#af87d7", "#5fafaf", "#d0d0d0",
	"#6c6c6c", "#ff8787", "#afd787", "#ffd787", "#87afff", "#d7afff", "#87d7d7", "#ffffff",
}

// xterm256ToCSS converts an xterm 256-colour index to a CSS colour.
func xterm256ToCSS(n int) string {
	switch {
	case n < 16:
		return ansiPalette[n]
	case n < 232:
		n -= 16
		r, g, b := n/36, (n/6)%6, n%6
		conv := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + v*40
		}
		return fmt.Sprintf("#%02x%02x%02x", conv(r), conv(g), conv(b))
	default:
		v := 8 + (n-232)*10
		return fmt.Sprintf("#%02x%02x%02x", v, v, v)
	}
}

// applySGR folds one escape sequence's parameters into the state.
func applySGR(params []int, st ansiState) ansiState {
	for i := 0; i < len(params); i++ {
		c := params[i]
		switch {
		case c == 0:
			st = ansiState{}
		case c == 1:
			st.bold = true
		case c == 2:
			st.dim = true
		case c == 3:
			st.italic = true
		case c == 4:
			st.underline = true
		case c >= 30 && c <= 37:
			st.fg = ansiPalette[c-30]
		case c >= 90 && c <= 97:
			st.fg = ansiPalette[c-90+8]
		case c >= 40 && c <= 47:
			st.bg = ansiPalette[c-40]
		case c >= 100 && c <= 107:
			st.bg = ansiPalette[c-100+8]
		case (c == 38 || c == 48) && i+1 < len(params):
			var colour string
			switch {
			case params[i+1] == 5 && i+2 < len(params):
				colour = xterm256ToCSS(params[i+2])
				i += 2
			case params[i+1] == 2 && i+4 < len(params):
				colour = fmt.Sprintf("#%02x%02x%02x", params[i+2], params[i+3], params[i+4])
				i += 4
			default:
				i++
				continue
			}
			if c == 38 {
				st.fg = colour
			} else {
				st.bg = colour
			}
		}
	}
	return st
}

// styleAttr renders the state as an inline CSS declaration, or "" when plain.
func (st ansiState) styleAttr() string {
	var parts []string
	if st.bold {
		parts = append(parts, "font-weight:700")
	}
	if st.dim {
		parts = append(parts, "opacity:.7")
	}
	if st.italic {
		parts = append(parts, "font-style:italic")
	}
	if st.underline {
		parts = append(parts, "text-decoration:underline")
	}
	if st.fg != "" {
		parts = append(parts, "color:"+st.fg)
	}
	if st.bg != "" {
		parts = append(parts, "background:"+st.bg)
	}
	return strings.Join(parts, ";")
}

// frameToHTML converts one rendered frame into a self-contained HTML page. The
// frame is a flat string of rows (the TUI never uses cursor addressing inside
// View()), so SGR state only needs to survive across rows, and every other
// escape sequence can be dropped.
func frameToHTML(frame string) string {
	var body strings.Builder
	st := ansiState{}
	flush := func(text string) {
		if text == "" {
			return
		}
		escaped := html.EscapeString(text)
		if attr := st.styleAttr(); attr != "" {
			fmt.Fprintf(&body, `<span style="%s">%s</span>`, attr, escaped)
			return
		}
		body.WriteString(escaped)
	}

	var run strings.Builder
	for i := 0; i < len(frame); {
		switch {
		case frame[i] == 0x1b && i+1 < len(frame) && frame[i+1] == '[':
			// CSI: consume through the final byte, apply SGR, drop the rest.
			j := i + 2
			for j < len(frame) && !(frame[j] >= 0x40 && frame[j] <= 0x7e) {
				j++
			}
			if j < len(frame) {
				if frame[j] == 'm' {
					flush(run.String())
					run.Reset()
					params := strings.Split(string(frame[i+2:j]), ";")
					codes := make([]int, 0, len(params))
					for _, p := range params {
						n := 0
						for _, r := range p {
							if r < '0' || r > '9' {
								n = 0
								break
							}
							n = n*10 + int(r-'0')
						}
						codes = append(codes, n)
					}
					st = applySGR(codes, st)
				}
				i = j + 1
				continue
			}
			i = j
		case frame[i] == 0x1b && i+1 < len(frame) && frame[i+1] == ']':
			// OSC hyperlink: skip to BEL or ST.
			j := i + 2
			for j < len(frame) && frame[j] != 0x07 && !(frame[j] == 0x1b && j+1 < len(frame) && frame[j+1] == '\\') {
				j++
			}
			if j < len(frame) {
				if frame[j] == 0x07 {
					j++
				} else {
					j += 2
				}
			}
			i = j
		case frame[i] == 0x1b:
			// Any other two-byte escape.
			i += 2
		case frame[i] == '\r':
			i++
		default:
			run.WriteByte(frame[i])
			i++
		}
	}
	flush(run.String())

	return `<!doctype html><meta charset="utf-8"><style>
html,body{margin:0;padding:0;background:#0b0b0f}
pre{margin:0;padding:10px 12px;color:#e8e8e8;white-space:pre;
font:14px/1.35 "SFMono-Regular",Menlo,Consolas,"DejaVu Sans Mono",monospace}
</style><pre>` + body.String() + "</pre>"
}

// --- Screenshots ----------------------------------------------------------

// shotScenario is one frame rendered to a PNG.
type shotScenario struct {
	name  string
	size  frameSize
	build func(w, h int) *TuiModel
}

var shotScenarios = []shotScenario{
	{"welcome", frameSize{80, 24}, frameWelcome},
	{"markdown", frameSize{80, 24}, frameMarkdown},
	{"todo", frameSize{80, 24}, frameTodo},
	{"dialog", frameSize{80, 24}, frameDialog},
	{"longoutput", frameSize{120, 40}, frameLongOutput},
}

// TestFrameScreenshots renders each scenario and writes a PNG next to the
// goldens' text form. Chromium is required; without one the test skips with the
// reason instead of failing, mirroring the repo's real-browser smoke test.
func TestFrameScreenshots(t *testing.T) {
	requireShot(t)

	browserPath := tool.FindBrowser()
	if browserPath == "" {
		t.Skip("no Chromium/Chrome installed (system or the Playwright cache)")
	}
	t.Logf("using browser %s", browserPath)

	dir := shotDir(t)
	l := newShotLauncher(t, browserPath)
	wsURL := l.MustLaunch()

	browser := rod.New().ControlURL(wsURL).MustConnect()
	defer browser.MustClose()

	for _, sc := range shotScenarios {
		t.Run(sc.name, func(t *testing.T) {
			var frame string
			withTrueColor(t, func() {
				frame = sc.build(sc.size.W, sc.size.H).View()
			})

			htmlPath := filepath.Join(dir, fmt.Sprintf("tinycode-frame-%s.html", sc.name))
			pngPath := filepath.Join(dir, fmt.Sprintf("tinycode-frame-%s-%s.png", sc.name, sc.size))
			if err := os.WriteFile(htmlPath, []byte(frameToHTML(frame)), 0o644); err != nil {
				t.Fatalf("write %s: %v", htmlPath, err)
			}

			page := browser.MustPage()
			defer page.MustClose()
			// 2x device pixel ratio: the text stays readable when the image is
			// zoomed in to inspect a column alignment.
			page.MustSetViewport(sc.size.W*9, sc.size.H*19, 2, false)
			page.MustNavigate("file://" + htmlPath)
			page.MustWaitLoad()

			shot := page.MustScreenshotFullPage()
			if err := os.WriteFile(pngPath, shot, 0o644); err != nil {
				t.Fatalf("write %s: %v", pngPath, err)
			}
			// A roughly blank page still produces a valid PNG; the size guard
			// catches "the frame did not render" without a pixel comparison.
			if len(shot) < 10_000 {
				t.Fatalf("%s is only %d bytes: the page probably rendered empty", pngPath, len(shot))
			}
			if !bytes.HasPrefix(shot, []byte("\x89PNG\r\n\x1a\n")) {
				t.Fatalf("%s is not a PNG", pngPath)
			}
			t.Logf("wrote %s (%d bytes)", pngPath, len(shot))
		})
	}
}

// --- Real binary under a PTY ---------------------------------------------

// assertBinaryFresh fails when bin/tinycode is older than the newest Go source in
// the repository. A stale binary makes this smoke test lie in both directions:
// it can pass while the tree is broken (a fix that was never built), and it can
// fail on a bug that is already fixed. The Makefile target always rebuilds;
// running `go test ./tui -run TestBinarySmoke` directly does not.
func assertBinaryFresh(t *testing.T, binary string, info os.FileInfo) {
	t.Helper()
	var newest string
	var newestTime time.Time
	root := filepath.Join("..")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a directory we cannot read cannot be newer
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "bin" || d.Name() == "testdata" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		stat, err := d.Info()
		if err != nil {
			return nil
		}
		if stat.ModTime().After(newestTime) {
			newestTime, newest = stat.ModTime(), path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan the sources for %s: %v", binary, err)
	}
	if newestTime.After(info.ModTime()) {
		t.Fatalf("%s is older than %s (%s): run `make build` first",
			binary, newest, newestTime.Format(time.RFC3339))
	}
}

// ptySmoke is one run of the built binary on a real PTY.
type ptySmoke struct {
	pty    *os.File
	cmd    *exec.Cmd
	out    *lockedBuffer
	exited chan error
}

// startBinaryPTY launches the built binary on a PTY. A nil size starts the pty
// without a window size, which is what a terminal that never got one reports
// (TIOCGWINSZ answers 0x0) - the geometry main.go must survive.
func startBinaryPTY(t *testing.T, size *pty.Winsize) *ptySmoke {
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

	// A hermetic HOME and working directory keep the run away from the user's
	// ~/.tinycode/config.json, so no MCP server or provider from a real config
	// can influence the smoke test.
	home := t.TempDir()
	work := t.TempDir()
	cmd := exec.Command(binary,
		"--api-key=shot-test",
		"--base-url=http://127.0.0.1:9/v1",
		"--model=shot-test",
		"--session-dir="+filepath.Join(home, "sessions"),
		"--log-level=error",
	)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "HOME="+home, "TERM=xterm-256color")

	out := &lockedBuffer{}
	var f *os.File
	if size == nil {
		f, err = pty.Start(cmd)
	} else {
		f, err = pty.StartWithSize(cmd, size)
	}
	if err != nil {
		t.Fatalf("start the binary on a PTY: %v", err)
	}

	smoke := &ptySmoke{pty: f, cmd: cmd, out: out, exited: make(chan error, 1)}
	t.Cleanup(func() {
		_ = f.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	// Drain the master continuously: if the PTY buffer fills, the child blocks
	// on write and the smoke test would deadlock instead of failing.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				_, _ = out.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() { smoke.exited <- cmd.Wait() }()
	return smoke
}

// quit sends the documented double Ctrl+C and requires a clean exit.
func (s *ptySmoke) quit(t *testing.T) {
	t.Helper()
	if _, err := s.pty.Write([]byte{3, 3}); err != nil {
		t.Fatalf("send Ctrl+C: %v", err)
	}
	select {
	case err := <-s.exited:
		if err != nil {
			t.Fatalf("the binary exited with %v\nstream tail:\n%s", err, stripANSIView(tail(s.out.String(), 800)))
		}
	case <-time.After(shotTimeout):
		t.Fatalf("the binary did not exit after Ctrl+C twice\nstream tail:\n%s", stripANSIView(tail(s.out.String(), 800)))
	}
}

// TestBinarySmokeUnderPTY starts the built binary on a real PTY at 80x24, waits
// for the startup frame, and quits with the documented double Ctrl+C. This is
// the only check that exercises main.go's wiring, the real renderer and a real
// terminal, and it asserts the stream carried colour.
func TestBinarySmokeUnderPTY(t *testing.T) {
	requireShot(t)

	smoke := startBinaryPTY(t, &pty.Winsize{Rows: 24, Cols: 80})
	waitForStream(t, smoke.out, "TinyCode")
	if !strings.Contains(smoke.out.String(), "\x1b[") {
		t.Errorf("the startup stream carried no escape sequences: termenv did not see the PTY as a colour terminal")
	}
	smoke.quit(t)
}

// TestBinarySmokeWithoutTerminalSize runs the binary on a PTY that was never
// given a window size. The kernel reports 0x0, so this is the outer-layer guard
// for the geometry clamp: before it, the first frame panicked in View() and the
// process exited through ErrProgramPanic instead of painting.
func TestBinarySmokeWithoutTerminalSize(t *testing.T) {
	requireShot(t)

	smoke := startBinaryPTY(t, nil)
	// The status bar is rendered outside the cell grid, so it survives a
	// one-cell-wide layout and proves a frame was produced.
	waitForStream(t, smoke.out, "plan")
	if got := stripANSIView(smoke.out.String()); strings.Contains(got, "panic:") {
		t.Fatalf("the size-less run panicked\nstream tail:\n%s", tail(got, 800))
	}
	smoke.quit(t)
}

// newShotLauncher builds the rod launcher used for screenshots: an installed
// browser, a headless session and a temp profile.
func newShotLauncher(t *testing.T, browserPath string) *launcher.Launcher {
	t.Helper()
	l := launcher.New().
		Bin(browserPath).
		Headless(true).
		UserDataDir(filepath.Join(shotDir(t), fmt.Sprintf("tinycode-shot-profile-%d", os.Getpid()))).
		Set(flags.NoSandbox).
		Append("disable-gpu").
		Append("disable-dev-shm-usage").
		Append("hide-scrollbars")
	t.Cleanup(l.Cleanup)
	return l
}

// waitForStream polls a captured terminal stream until it contains want.
func waitForStream(t *testing.T, out *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(shotTimeout)
	for {
		if strings.Contains(out.String(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stream never contained %q within %s\nstream tail:\n%s",
				want, shotTimeout, stripANSIView(tail(out.String(), 800)))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tail returns the last n bytes of s.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
