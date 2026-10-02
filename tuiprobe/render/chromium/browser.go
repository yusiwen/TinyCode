// Package chromium renders a screen by asking a real browser for a screenshot.
//
// It is the optional renderer: the pure-Go one needs nothing installed, while this
// path buys pixel-exact CSS, system fonts and emoji at the cost of a browser
// launch. Everything about finding and probing that browser is here, because those
// are the parts that were learned the hard way:
//
//   - a candidate that cannot answer `--version` is not handed to the renderer
//     (a broken shim on a CI runner looks like a hang, not an error);
//   - a probe that runs out of its budget is not cached as permanently unusable,
//     and a *successful* verdict expires too, so a browser installed later is
//     found without restarting the tool;
//   - the Playwright headless shell is preferred for a screenshot: the full
//     desktop build produced no DOM at all on macOS while the shell of the same
//     revision answered in about a second.
package chromium

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultProbeTimeout bounds one `--version` probe. It is generous because a
// browser on a loaded machine is slow to answer, and rejecting a working browser
// is worse than waiting.
var DefaultProbeTimeout = 5 * time.Second

// ProbeRetryAfter is how long a probe verdict is trusted. A successful probe is
// re-checked eventually so a browser that appears later is found; a timed-out
// probe is retried after the same window instead of being condemned forever.
var ProbeRetryAfter = 30 * time.Second

// systemCommands are tried in order after the environment overrides.
var systemCommands = []string{
	"chromium-headless-shell",
	"chromium",
	"chromium-browser",
	"google-chrome",
	"google-chrome-stable",
	"microsoft-edge",
	"microsoft-edge-stable",
}

// darwinAppPaths are the macOS bundles, in the same order of preference.
var darwinAppPaths = []string{
	"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
	"/Applications/Chromium.app/Contents/MacOS/Chromium",
	"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
}

// Finder looks for a usable browser.
//
// Every dependency is a field so the search can be tested without a browser, a
// cache or a network: the tests drive it with fixture directories and a stub
// `--version` responder.
type Finder struct {
	Getenv   func(string) string
	LookPath func(string) (string, error)
	// CacheDir is the Playwright cache root; empty means the per-OS default.
	CacheDir string
	// Stat reports whether a path is a regular file.
	Stat func(string) (os.FileInfo, error)
	// AppPaths overrides the macOS bundle list; nil means the built-in one, and an
	// empty non-nil slice means none (which is what a hermetic test wants).
	AppPaths []string
	// Probe answers whether a browser at that path is usable.
	Probe func(path string) bool
	// Now is the clock, for the verdict expiry.
	Now func() time.Time

	mu     sync.Mutex
	probed map[string]probeVerdict
}

type probeVerdict struct {
	usable   bool
	probedAt time.Time
}

// Default is the finder the renderers use.
var Default = &Finder{}

// Find returns the path of a usable browser, or "" when there is none.
func (f *Finder) Find() string {
	for _, candidate := range f.candidates() {
		if f.usable(candidate) {
			return candidate
		}
	}
	return ""
}

// candidates lists every path worth probing, best first.
func (f *Finder) candidates() []string {
	var out []string
	seen := map[string]bool{}
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}

	for _, name := range []string{"CHROME_PATH", "CHROME"} {
		add(f.getenv(name))
	}
	for _, name := range systemCommands {
		if path, err := f.lookPath(name); err == nil {
			add(path)
		}
	}
	for _, path := range f.appPaths() {
		if info, err := f.stat(path); err == nil && info.Mode().IsRegular() {
			add(path)
		}
	}
	for _, path := range f.playwrightCandidates() {
		add(path)
	}
	return out
}

// playwrightCandidates finds the browsers Playwright unpacks, the headless shell
// first: for a screenshot it is the build that works.
func (f *Finder) playwrightCandidates() []string {
	root := f.CacheDir
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		if runtime.GOOS == "darwin" {
			root = filepath.Join(home, "Library", "Caches", "ms-playwright")
		} else {
			root = filepath.Join(home, ".cache", "ms-playwright")
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	type pkg struct {
		name  string
		rev   int
		shell bool
	}
	var pkgs []pkg
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() {
			continue
		}
		switch {
		case strings.HasPrefix(name, "chromium_headless_shell-"):
			pkgs = append(pkgs, pkg{name: name, rev: revision(name, "chromium_headless_shell-"), shell: true})
		case strings.HasPrefix(name, "chromium-"):
			pkgs = append(pkgs, pkg{name: name, rev: revision(name, "chromium-")})
		}
	}
	// The shell first, then newest revision: the revision is parsed rather than
	// compared as text, where "chromium-999" would outrank "chromium-1000".
	sort.Slice(pkgs, func(i, j int) bool {
		if pkgs[i].shell != pkgs[j].shell {
			return pkgs[i].shell
		}
		if pkgs[i].rev != pkgs[j].rev {
			return pkgs[i].rev > pkgs[j].rev
		}
		return pkgs[i].name > pkgs[j].name
	})

	var out []string
	for _, p := range pkgs {
		out = append(out, playwrightPaths(root, p.name)...)
	}
	return out
}

// playwrightPaths lists the layouts one Playwright package can have.
func playwrightPaths(root, dir string) []string {
	rel := func(parts ...string) string {
		return filepath.Join(append([]string{root, dir}, parts...)...)
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{
			rel("chrome-headless-shell-mac-arm64", "chrome-headless-shell"),
			rel("chrome-headless-shell-mac-x64", "chrome-headless-shell"),
			rel("chrome-mac-arm64", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing"),
			rel("chrome-mac-x64", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing"),
			rel("chrome-mac", "Chromium.app", "Contents", "MacOS", "Chromium"),
		}
	case "windows":
		return []string{
			rel("chrome-headless-shell-win64", "chrome-headless-shell.exe"),
			rel("chrome-win", "chrome.exe"),
		}
	default:
		return []string{
			rel("chrome-headless-shell-linux64", "chrome-headless-shell"),
			rel("chrome-linux64", "chrome"),
			rel("chrome-linux", "chrome"),
		}
	}
}

// usable reports whether a candidate answers `--version`, memoizing the verdict
// until it expires.
func (f *Finder) usable(path string) bool {
	info, err := f.stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}

	now := f.now()
	f.mu.Lock()
	if f.probed == nil {
		f.probed = map[string]probeVerdict{}
	}
	if verdict, ok := f.probed[path]; ok && now.Sub(verdict.probedAt) < ProbeRetryAfter {
		f.mu.Unlock()
		return verdict.usable
	}
	f.mu.Unlock()

	usable := f.probePath(path)
	f.mu.Lock()
	f.probed[path] = probeVerdict{usable: usable, probedAt: now}
	f.mu.Unlock()
	return usable
}

// probePath answers whether a candidate is usable, through the injected probe when
// a test supplied one.
func (f *Finder) probePath(path string) bool {
	if f.Probe != nil {
		return f.Probe(path)
	}
	return f.probe(path)
}

// probe runs `--version` under a bounded timeout. A candidate that hangs is not a
// candidate: a distro shim whose daemon is unreachable does not fail fast, which is
// how a smoke test once spent 35 seconds inside discovery.
func (f *Finder) probe(path string) bool {
	timeout := DefaultProbeTimeout
	done := make(chan error, 1)
	cmd := exec.Command(path, "--version")
	cmd.Stdout, cmd.Stderr = nil, nil
	if err := cmd.Start(); err != nil {
		return false
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return false
	}
}

// appPaths is the macOS bundle list, overridable so a test does not accidentally
// discover the browser installed on the machine running it.
func (f *Finder) appPaths() []string {
	if f.AppPaths != nil {
		return f.AppPaths
	}
	if runtime.GOOS != "darwin" {
		return nil
	}
	return darwinAppPaths
}

func (f *Finder) getenv(key string) string {
	if f.Getenv != nil {
		return f.Getenv(key)
	}
	return os.Getenv(key)
}

func (f *Finder) lookPath(name string) (string, error) {
	if f.LookPath != nil {
		return f.LookPath(name)
	}
	return exec.LookPath(name)
}

func (f *Finder) stat(path string) (os.FileInfo, error) {
	if f.Stat != nil {
		return f.Stat(path)
	}
	return os.Stat(path)
}

func (f *Finder) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// revision parses the number out of "chromium-1243".
func revision(name, prefix string) int {
	n, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
	if err != nil {
		return 0
	}
	return n
}
