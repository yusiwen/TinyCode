package chromium

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/TinyCode/tuiprobe/screen"
)

// fixture writes a fake browser that answers `--version` (or does not).
func fixture(t *testing.T, dir, name string, usable bool) string {
	t.Helper()
	path := filepath.Join(dir, name)
	body := "#!/bin/sh\nexit 1\n"
	if usable {
		body = "#!/bin/sh\necho 'Chromium 124.0.0.0'\n"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFindPrefersTheEnvironmentThenThePath(t *testing.T) {
	dir := t.TempDir()
	override := fixture(t, dir, "my-chrome", true)
	fromPath := fixture(t, dir, "chromium", true)

	f := &Finder{
		Getenv:   func(key string) string { return map[string]string{"CHROME_PATH": override}[key] },
		LookPath: func(name string) (string, error) { return fromPath, nil },
		Stat:     os.Stat,
		AppPaths: []string{},
		CacheDir: t.TempDir(),
		Now:      time.Now,
	}
	if got := f.Find(); got != override {
		t.Errorf("Find = %q, want the CHROME_PATH override %q", got, override)
	}

	// Without an override the PATH candidate wins.
	f.Getenv = func(string) string { return "" }
	if got := f.Find(); got != fromPath {
		t.Errorf("Find = %q, want the PATH candidate %q", got, fromPath)
	}
}

// TestFindSkipsABrokenCandidate is the #26 lesson: a shim that cannot answer
// `--version` must not be handed to the renderer.
func TestFindSkipsABrokenCandidate(t *testing.T) {
	dir := t.TempDir()
	broken := fixture(t, dir, "chromium", false)
	good := fixture(t, dir, "google-chrome", true)

	f := &Finder{
		Getenv:   func(string) string { return "" },
		LookPath: func(string) (string, error) { return broken, nil }, // always the broken one
		Stat:     os.Stat,
		AppPaths: []string{},
		CacheDir: t.TempDir(),
		Now:      time.Now,
	}
	if got := f.Find(); got != "" {
		t.Errorf("Find = %q, want nothing: the only candidate cannot answer --version", got)
	}

	// A second candidate that works is found when the first is not preferred.
	f.LookPath = func(name string) (string, error) {
		if name == "chromium" {
			return broken, nil
		}
		return good, nil
	}
	if got := f.Find(); got != good {
		t.Errorf("Find = %q, want the working candidate %q", got, good)
	}
}

// TestProbeVerdictExpires is the other half of #26: a probe that ran out of budget
// must not be cached as permanently unusable, and a success must be re-checked
// eventually so a browser installed later is found.
func TestProbeVerdictExpires(t *testing.T) {
	dir := t.TempDir()
	path := fixture(t, dir, "chromium", true)

	calls := 0
	now := time.Now()
	usable := false
	f := &Finder{
		Getenv:   func(string) string { return "" },
		LookPath: func(string) (string, error) { return path, nil },
		Stat:     os.Stat,
		AppPaths: []string{},
		CacheDir: t.TempDir(),
		Now:      func() time.Time { return now },
		Probe: func(string) bool {
			calls++
			return usable
		},
	}

	if f.Find() != "" {
		t.Fatal("an unusable probe must not be returned")
	}
	// Within the window the verdict is reused: no second probe.
	f.Find()
	if calls != 1 {
		t.Errorf("probe ran %d times inside the retry window, want 1", calls)
	}

	// After the window the browser is probed again, and now it works.
	now = now.Add(ProbeRetryAfter + time.Second)
	usable = true
	if got := f.Find(); got != path {
		t.Errorf("Find = %q, want %q after the verdict expired", got, path)
	}
	if calls != 2 {
		t.Errorf("probe ran %d times, want 2 (one per expiry window)", calls)
	}
}

func TestPlaywrightCandidatesPreferTheHeadlessShell(t *testing.T) {
	cache := t.TempDir()

	// Build the fixtures at the paths this OS actually looks for, so the test says
	// something on every platform.
	shellPath := playwrightPaths(cache, "chromium_headless_shell-1243")[0]
	fullPath := playwrightPaths(cache, "chromium-1243")[0]
	if shellPath == fullPath {
		t.Fatal("the two packages resolved to the same path")
	}
	fixture(t, filepath.Dir(shellPath), filepath.Base(shellPath), true)
	fixture(t, filepath.Dir(fullPath), filepath.Base(fullPath), true)

	f := &Finder{
		CacheDir: cache,
		Getenv:   func(string) string { return "" },
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		AppPaths: []string{},
		Stat:     os.Stat, Now: time.Now,
	}
	if got := f.Find(); got != shellPath {
		t.Errorf("Find = %q, want the headless shell %q (it is the build that can take a screenshot)", got, shellPath)
	}
}

// TestRenderNeedsABrowser keeps the contract honest: with no browser, the renderer
// refuses with a message that says what to do.
func TestRenderNeedsABrowser(t *testing.T) {
	f := &Finder{Getenv: func(string) string { return "" }, LookPath: func(string) (string, error) { return "", os.ErrNotExist }, CacheDir: t.TempDir(), Stat: os.Stat, AppPaths: []string{}, Now: time.Now}
	saved := Default
	Default = f
	t.Cleanup(func() { Default = saved })

	if _, err := New(Options{}); err == nil {
		t.Fatal("New must fail when no browser is available")
	} else if want := "no usable browser"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %v, want it to mention %q", err, want)
	}
}

// newScreen replays a stream into a buffer of the given size.
func newScreen(t *testing.T, cols, rows int, stream string) *screen.Buffer {
	t.Helper()
	b := screen.New(cols, rows)
	if _, err := b.Write([]byte(stream)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	return b
}

func TestBoundsFollowTheGeometry(t *testing.T) {
	minW, maxW, minH, maxH := Bounds(80, 24, 1)
	if minW != 80*CellWidth || minH != 24*CellHeight {
		t.Errorf("bounds start at %dx%d, want %dx%d", minW, minH, 80*CellWidth, 24*CellHeight)
	}
	if maxW-minW > 24 || maxH-minH > 24 {
		t.Errorf("the tolerance is %d px wide and %d px tall, want at most 24", maxW-minW, maxH-minH)
	}
	// A wider screen allows a proportionally wider image: the bound is a function
	// of the geometry, not a constant.
	wideMinW, _, _, _ := Bounds(120, 24, 1)
	if wideMinW <= minW {
		t.Errorf("120 columns allow %d px, 80 columns allow %d: the bound must grow", wideMinW, minW)
	}
}

// TestScreenshotWithARealBrowser is gated: it needs a browser, and skips with the
// reason when there is none (the same contract the harness has).
func TestScreenshotWithARealBrowser(t *testing.T) {
	if os.Getenv("TUIPROBE_BROWSER_TEST") == "" {
		t.Skip("skipping: set TUIPROBE_BROWSER_TEST=1 to run the real-browser screenshot test")
	}
	browser := Default.Find()
	if browser == "" {
		t.Skip("no Chromium/Chrome found (CHROME_PATH, PATH or the Playwright cache)")
	}

	renderer, err := New(Options{Browser: browser, Timeout: 60 * time.Second})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	terminal := newScreen(t, 30, 3, "\x1b[1;36mTinyCode\033[0m\r\n\x1b[32mready\x1b[0m\r\n\x1b[4;31munderlined red\x1b[0m")
	data, width, height, err := renderer.PNG(terminal, 30, 3)
	if err != nil {
		t.Fatalf("PNG: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("no image data")
	}
	minW, maxW, minH, maxH := Bounds(30, 3, 1)
	if width < minW || width > maxW || height < minH || height > maxH {
		t.Errorf("image %dx%d is outside %dx%d..%dx%d", width, height, minW, minH, maxW, maxH)
	}
	if err := os.WriteFile(filepath.Join(os.TempDir(), "tuiprobe-browser-shot.png"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("browser %s produced %dx%d (%d bytes)", browser, width, height, len(data))
}

// TestHTMLLaysEveryCellOutAbsolutely pins the layout rule, and the bug that made it
// one: with per-cell inline-blocks the glyphs advanced by the font's own width and
// overlapped, so "TinyCode TUI" rendered as "TnCd U". A browser only behaves like a
// terminal grid when every cell is told where it is.
func TestHTMLLaysEveryCellOutAbsolutely(t *testing.T) {
	b := screen.New(3, 2)
	if _, err := b.Write([]byte("\x1b[31mA\x1b[0mB\r\nC")); err != nil {
		t.Fatal(err)
	}
	doc := (&Renderer{Browser: "unused"}).HTML(b, 3, 2)

	for _, want := range []string{
		"left:0px;top:0px",  // A
		"left:9px;top:0px",  // B
		"left:0px;top:18px", // C
		"color:#d75f5f",     // the styled cell keeps its colour
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("layout misses %q:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, "inline-block") {
		t.Error("the inline-block layout is back: it overlapped glyphs whenever the face was wider than the cell")
	}
	// The document is sized to the geometry in CSS pixels.
	if !strings.Contains(doc, "width: 27px; height: 36px") {
		t.Errorf("the page is not sized to 3x2 cells (27x36 CSS px):\n%s", doc)
	}
}
