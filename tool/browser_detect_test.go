package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/launcher"
)

// writeFixture creates a file at dir/rel, creating parent directories.
func writeFixture(t *testing.T, dir, rel string) string {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return path
}

// TestFindPlaywrightBrowser covers the layouts Playwright uses. The old
// detection only knew `chrome-mac/Chromium.app`, so every current installation
// was invisible and the browser tool silently downloaded its own Chromium.
func TestFindPlaywrightBrowser(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		fixture string
		want    string
	}{
		{
			name:    "darwin google chrome for testing arm64",
			goos:    "darwin",
			fixture: "chromium-1243/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
			want:    "chromium-1243/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
		},
		{
			name:    "darwin google chrome for testing x64",
			goos:    "darwin",
			fixture: "chromium-1243/chrome-mac-x64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
			want:    "chromium-1243/chrome-mac-x64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
		},
		{
			name:    "darwin legacy chromium.app",
			goos:    "darwin",
			fixture: "chromium-1000/chrome-mac/Chromium.app/Contents/MacOS/Chromium",
			want:    "chromium-1000/chrome-mac/Chromium.app/Contents/MacOS/Chromium",
		},
		{
			name:    "darwin headless shell only",
			goos:    "darwin",
			fixture: "chromium_headless_shell-1243/chrome-headless-shell-mac-arm64/chrome-headless-shell",
			want:    "chromium_headless_shell-1243/chrome-headless-shell-mac-arm64/chrome-headless-shell",
		},
		{
			name:    "linux current",
			goos:    "linux",
			fixture: "chromium-1243/chrome-linux64/chrome",
			want:    "chromium-1243/chrome-linux64/chrome",
		},
		{
			name:    "linux legacy",
			goos:    "linux",
			fixture: "chromium-1000/chrome-linux/chrome",
			want:    "chromium-1000/chrome-linux/chrome",
		},
		{
			name:    "windows full browser preferred over shell",
			goos:    "windows",
			fixture: "chromium-1243/chrome-win/chrome.exe",
			want:    "chromium-1243/chrome-win/chrome.exe",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cache := t.TempDir()
			writeFixture(t, cache, tc.fixture)

			got := findPlaywrightBrowser(cache, tc.goos)
			if got != filepath.Join(cache, tc.want) {
				t.Fatalf("findPlaywrightBrowser = %q, want %q", got, filepath.Join(cache, tc.want))
			}
		})
	}
}

// TestFindPlaywrightBrowserPrefersNewest checks the revision ordering and that a
// package directory without a browser binary is skipped.
func TestFindPlaywrightBrowserPrefersNewest(t *testing.T) {
	cache := t.TempDir()
	writeFixture(t, cache, "chromium-1000/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing")
	newest := writeFixture(t, cache, "chromium-1243/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing")
	if err := os.MkdirAll(filepath.Join(cache, "chromium-1300", "chrome-mac-arm64"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, cache, "chromium-1100/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing")
	// A three-digit revision must not outrank a four-digit one.
	writeFixture(t, cache, "chromium-999/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing")
	writeFixture(t, cache, "chromium_headless_shell-1243/chrome-headless-shell-mac-arm64/chrome-headless-shell")

	if got := findPlaywrightBrowser(cache, "darwin"); got != newest {
		t.Fatalf("findPlaywrightBrowser = %q, want the newest full browser %q", got, newest)
	}
}

// TestFindPlaywrightBrowserEmpty covers the no-installation cases.
func TestFindPlaywrightBrowserEmpty(t *testing.T) {
	if got := findPlaywrightBrowser(filepath.Join(t.TempDir(), "missing"), "darwin"); got != "" {
		t.Errorf("missing cache dir = %q, want empty", got)
	}

	cache := t.TempDir()
	writeFixture(t, cache, "ffmpeg-1011/ffmpeg-mac")
	if got := findPlaywrightBrowser(cache, "darwin"); got != "" {
		t.Errorf("cache without a browser = %q, want empty", got)
	}
}

// TestPlaywrightCacheDir pins the per-platform cache location.
func TestPlaywrightCacheDir(t *testing.T) {
	if got := playwrightCacheDir("/home/u", "darwin"); got != "/home/u/Library/Caches/ms-playwright" {
		t.Errorf("darwin cache dir = %q", got)
	}
	if got := playwrightCacheDir("/home/u", "linux"); got != "/home/u/.cache/ms-playwright" {
		t.Errorf("linux cache dir = %q", got)
	}
}

// --- Discovery order and usability probing (issue #15) --------------------

// writeScript creates an executable fixture at path with the given body.
func writeScript(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// linkProbeBinary points path at a native binary that exits 0 whatever arguments
// it is given, so probing the fixture costs one exec. A shell script costs ~350 ms
// to start on a loaded macOS box, and that fixture cost - not the production code
// - is what made the hanging-shim test timing-sensitive: the "working" browser had
// to exec inside a budget the hanging shim had just finished spending.
func linkProbeBinary(t *testing.T, path string) string {
	t.Helper()
	for _, target := range []string{"/usr/bin/true", "/bin/true"} {
		if _, err := os.Stat(target); err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("mkdir for %s: %v", path, err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatalf("symlink %s -> %s: %v", path, target, err)
		}
		return path
	}
	t.Skip("no /usr/bin/true or /bin/true to link a native probe fixture to")
	return ""
}

// TestBrowserCandidatesOrder pins the preference order: an explicit override,
// then the system commands in their declared order, then the Playwright cache,
// with duplicates dropped.
func TestBrowserCandidatesOrder(t *testing.T) {
	system := func(paths map[string]string) func(string) (string, error) {
		return func(name string) (string, error) {
			if p, ok := paths[name]; ok {
				return p, nil
			}
			return "", os.ErrNotExist
		}
	}
	env := func(values map[string]string) func(string) string {
		return func(name string) string { return values[name] }
	}

	got := browserCandidates(
		env(map[string]string{"CHROME_PATH": "/opt/explicit/chrome"}),
		system(map[string]string{"chromium-browser": "/usr/bin/chromium-browser"}),
		func() string { return "/cache/chromium" },
	)
	want := []string{"/opt/explicit/chrome", "/usr/bin/chromium-browser", "/cache/chromium"}
	if !slices.Equal(got, want) {
		t.Errorf("candidates = %q, want %q", got, want)
	}

	// CHROME is the second override and is only used when CHROME_PATH is unset.
	got = browserCandidates(
		env(map[string]string{"CHROME": "/opt/chrome"}),
		system(nil),
		func() string { return "" },
	)
	if want := []string{"/opt/chrome"}; !slices.Equal(got, want) {
		t.Errorf("candidates = %q, want %q", got, want)
	}

	// A path that is both an override and a system command is offered once.
	got = browserCandidates(
		env(map[string]string{"CHROME_PATH": "/usr/bin/chromium-browser"}),
		system(map[string]string{"chromium-browser": "/usr/bin/chromium-browser"}),
		func() string { return "" },
	)
	if want := []string{"/usr/bin/chromium-browser"}; !slices.Equal(got, want) {
		t.Errorf("candidates = %q, want %q (deduplicated)", got, want)
	}
}

// TestFirstUsableBrowserSkipsBrokenCandidate is the unit form of the CI failure:
// the first candidate exists but does not work, and the working one behind it
// must be chosen instead of being handed to an extractor that then hangs.
func TestFirstUsableBrowserSkipsBrokenCandidate(t *testing.T) {
	probe := func(path string) bool { return path == "/good" }

	if got := firstUsableBrowser([]string{"/broken", "/good"}, probe); got != "/good" {
		t.Errorf("firstUsableBrowser = %q, want /good", got)
	}
	if got := firstUsableBrowser([]string{"/broken", "/also-broken"}, probe); got != "" {
		t.Errorf("firstUsableBrowser = %q, want empty when nothing is usable", got)
	}
	if got := firstUsableBrowser(nil, probe); got != "" {
		t.Errorf("firstUsableBrowser = %q, want empty for no candidates", got)
	}
}

// TestBrowserUsableProbesVersion covers the probe itself: an answer means
// usable, a non-zero exit does not, and a binary that never answers is rejected
// once the timeout fires rather than blocking the caller.
func TestBrowserUsableProbesVersion(t *testing.T) {
	previousTimeout := browserProbeTimeout
	// The usable fixture is a native binary, so its probe costs one exec and the
	// budget only has to expire for the fixture that never answers.
	browserProbeTimeout = 2 * time.Second
	t.Cleanup(func() { browserProbeTimeout = previousTimeout })

	dir := t.TempDir()
	ok := linkProbeBinary(t, filepath.Join(dir, "ok"))
	bad := writeScript(t, filepath.Join(dir, "bad"), "#!/bin/sh\nexit 1\n")
	hang := writeScript(t, filepath.Join(dir, "hang"), "#!/bin/sh\nexec /bin/sleep 30\n")

	if !browserUsable(ok) {
		t.Errorf("browserUsable(%s) = false, want true", ok)
	}
	if browserUsable(bad) {
		t.Errorf("browserUsable(%s) = true, want false for a non-zero exit", bad)
	}
	start := time.Now()
	if browserUsable(hang) {
		t.Errorf("browserUsable(%s) = true, want false for a binary that never answers", hang)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the probe took %s, want it bounded by browserProbeTimeout", elapsed)
	}

	// The answer is memoized, so removing the binary does not change it: this is
	// what keeps discovery from re-probing on every extraction. (A timed-out
	// verdict expires instead of standing for the process - see
	// TestBrowserUsableRetriesAfterTimedOutProbe.)
	if err := os.Remove(ok); err != nil {
		t.Fatal(err)
	}
	if !browserUsable(ok) {
		t.Errorf("browserUsable(%s) after removal = false, want the cached true", ok)
	}
}

// TestBrowserUsableRetriesAfterTimedOutProbe covers the other half of issue #26:
// a probe that ran out of budget must not stand for the life of the process. The
// verdict used to be memoized like an answer, so one loaded moment rejected a
// working browser for every extraction that followed.
func TestBrowserUsableRetriesAfterTimedOutProbe(t *testing.T) {
	previousTimeout, previousRetry := browserProbeTimeout, browserProbeRetryAfter
	browserProbeTimeout = 1500 * time.Millisecond
	// A cooldown long enough that no assertion below depends on wall-clock
	// timing; the second half sets it to zero to reach the expiry immediately.
	browserProbeRetryAfter = time.Hour
	t.Cleanup(func() {
		browserProbeTimeout, browserProbeRetryAfter = previousTimeout, previousRetry
	})

	path := writeScript(t, filepath.Join(t.TempDir(), "chrome"), "#!/bin/sh\nexec /bin/sleep 30\n")
	if browserUsable(path) {
		t.Fatalf("browserUsable(%s) = true, want a candidate that never answers rejected", path)
	}

	// The candidate is replaced by one that answers immediately. A fresh probe
	// would accept it now, so a false here proves the timed-out verdict is still
	// in force inside its cooldown - which is what a burst of extractions relies
	// on to pay one timeout instead of one per call.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	linkProbeBinary(t, path)
	if browserUsable(path) {
		t.Errorf("browserUsable(%s) inside the retry cooldown = true, want the timed-out verdict", path)
	}

	// Past the cooldown the path is probed again and accepted, so a browser that
	// was slow to start on a loaded machine is not disabled for good.
	browserProbeRetryAfter = 0
	if !browserUsable(path) {
		t.Errorf("browserUsable(%s) past the retry cooldown = false, want a fresh probe to accept it", path)
	}
}

// TestFindBrowserSkipsHangingShim reproduces the runner's layout: the first
// command in browserCommands is a shim that never answers --version, and the
// browser the CI action installed is behind it on PATH. The shim must be
// skipped, which is what a bare LookPath loop got wrong.
func TestFindBrowserSkipsHangingShim(t *testing.T) {
	previousTimeout := browserProbeTimeout
	// One candidate must clear the timeout (the shim hangs) and the next must fit
	// inside it. The working fixture is a native binary, so its probe costs one
	// exec and the budget has orders of magnitude of headroom rather than the
	// ~350 ms a shell script used to need here.
	browserProbeTimeout = 1500 * time.Millisecond
	t.Cleanup(func() { browserProbeTimeout = previousTimeout })

	dir := t.TempDir()
	// /bin/sleep by absolute path: PATH is rewritten to the fixture directory
	// below, so a bare `sleep` would fail instantly instead of hanging.
	shim := writeScript(t, filepath.Join(dir, "chromium-browser"), "#!/bin/sh\nexec /bin/sleep 30\n")
	working := linkProbeBinary(t, filepath.Join(dir, "chrome"))

	t.Setenv("PATH", dir)
	t.Setenv("CHROME_PATH", "")
	t.Setenv("CHROME", "")

	// The shim is still the first candidate - the order is unchanged; the probe
	// is what rejects it.
	candidates := browserCandidates(os.Getenv, exec.LookPath, func() string { return "" })
	if len(candidates) == 0 || candidates[0] != shim {
		t.Fatalf("candidates = %q, want the shim first", candidates)
	}

	if got := findBrowser(); got != working {
		t.Errorf("findBrowser = %q, want the working browser %q", got, working)
	}

	// And the explicit override beats both, without probing anything else.
	t.Setenv("CHROME_PATH", working)
	if got := findBrowser(); got != working {
		t.Errorf("findBrowser with CHROME_PATH = %q, want %q", got, working)
	}
}

// TestBrowserContainerFlags pins the launched argument list against the two
// container realities the smoke test hit: without --no-sandbox Chromium aborts in
// the zygote and never publishes a debug URL, and without --disable-dev-shm-usage
// it dies where /dev/shm is small. The assertion is on the effective list, so it
// does not matter whether this code or rod's defaults supplied the second one.
func TestBrowserContainerFlags(t *testing.T) {
	args := browserContainerFlags(launcher.New()).FormatArgs()
	for _, want := range []string{"--no-sandbox", "--disable-dev-shm-usage"} {
		if !slices.Contains(args, want) {
			t.Errorf("args are missing %s: %q", want, args)
		}
	}
}

// TestExecBrowserArgs pins the --dump-dom argument list. Two of the assertions
// are findings from the browser job: --single-process aborts Chromium on the
// runner, and the throwaway profile is what keeps extraction out of the user's
// real profile (and working where that profile is not writable).
func TestExecBrowserArgs(t *testing.T) {
	args := execBrowserArgs(
		"/tmp/tinycode-chrome-1",
		"http://example.com/page",
		"http://127.0.0.1:9",
		"MAP example.com 203.0.113.1",
	)

	for _, want := range []string{
		"--headless",
		"--disable-gpu",
		"--no-sandbox",
		"--disable-breakpad",
		"--user-data-dir=/tmp/tinycode-chrome-1",
		"--host-resolver-rules=MAP example.com 203.0.113.1",
		"--proxy-server=http://127.0.0.1:9",
	} {
		if !slices.Contains(args, want) {
			t.Errorf("args are missing %q: %q", want, args)
		}
	}

	// --single-process is documented as unsupported and aborts Chromium while
	// rendering, which is how the exec half of the smoke test died.
	if slices.Contains(args, "--single-process") {
		t.Errorf("args contain --single-process: %q", args)
	}

	// Chromium expects the URL last, behind --dump-dom.
	if len(args) < 2 || args[len(args)-2] != "--dump-dom" || args[len(args)-1] != "http://example.com/page" {
		t.Errorf("args do not end with --dump-dom <url>: %q", args)
	}

	// No proxy and no rule: neither flag may appear out of nowhere.
	bare := execBrowserArgs("/tmp/p", "http://example.com", "", "")
	for _, arg := range bare {
		if strings.HasPrefix(arg, "--proxy-server=") || strings.HasPrefix(arg, "--host-resolver-rules=") {
			t.Errorf("bare args carry %q, want neither proxy nor pinning flags: %q", arg, bare)
		}
	}
}

// TestLastLines covers the bounded stderr tail that makes a Chromium abort
// diagnosable in CI.
func TestLastLines(t *testing.T) {
	if got := lastLines("a\n\nb\nc\n", 2); got != "b | c" {
		t.Errorf("lastLines = %q, want %q", got, "b | c")
	}
	if got := lastLines("", 3); got != "" {
		t.Errorf("lastLines of an empty string = %q, want empty", got)
	}
	long := strings.Repeat("x", 900)
	if got := lastLines(long, 1); len(got) != 500 {
		t.Errorf("lastLines kept %d bytes, want it capped at 500", len(got))
	}
}
