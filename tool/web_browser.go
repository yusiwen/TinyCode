package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"

	"github.com/yusiwen/tinycode/internal/browserproxy"
	"github.com/yusiwen/tinycode/internal/netsafe"
	"github.com/yusiwen/tinycode/tlog"
)

// browserProxyFlags returns the Chromium flags that route the browser through
// the loopback filtering proxy, as flag/value pairs plus bare switches.
//
// Chromium bypasses any proxy for loopback addresses by default, and the proxy
// is precisely what stands between the browser and an internal service, so that
// shortcut is disabled with the magic "<-loopback>" entry.
//
// The rest of the list closes channels that never ask a proxy at all (issue #8):
//
//   - QUIC/HTTP3 travels over UDP, so it would leave without ever asking the
//     proxy; it is turned off.
//   - WebRTC/STUN is the other one: an ICE candidate sends UDP straight to an
//     address the page chose, which is a way to probe the internal network
//     without touching HTTP. `disable_non_proxied_udp` makes WebRTC use only
//     proxied transports (or none), so the filtering proxy stays the single
//     exit.
//   - Chromium's own background traffic (component updates, domain reliability,
//     safe-browsing pings, sync) is not part of the page and has no business
//     reaching the network during a crawl.
//
// Every one of these is inert where it does not apply: Chromium ignores a switch
// it does not know, and this list is pinned by the argument tests rather than
// exercised against a real peer connection.
func browserProxyFlags(proxyURL string) (valued [][2]string, bare []string) {
	return [][2]string{
		{"proxy-server", proxyURL},
		{"proxy-bypass-list", "<-loopback>"},
		{"force-webrtc-ip-handling-policy", "disable_non_proxied_udp"},
	}, []string{"disable-quic", "disable-background-networking"}
}

// errBrowserSandboxUnavailable is returned when the browser cannot run inside the
// sandbox the tool promises to enforce. Both browser paths fail closed on it: the
// filtering proxy resolves, validates and pins every hostname the browser
// contacts, including the CONNECT tunnels the rod request interceptor cannot see
// into, so a crawl without it would silently be a weaker path than the plain HTTP
// fetch and not a browser fallback at all (issue #8).
var errBrowserSandboxUnavailable = errors.New(
	"browser sandbox unavailable: the filtering proxy did not start; " +
		"refusing to crawl without it (see the web.browser log entry for the cause)")

// browserSandboxConfig is how the browser paths start the filtering proxy. The
// zero value is the production policy: enforce the SSRF rules unless the
// package-wide test hook disables them wholesale, and grant no loopback
// exemption. The real-browser smoke test replaces it to keep the rules enforced
// while it serves its page from a loopback listener.
var browserSandboxConfig struct {
	// forceEnforce keeps the proxy's policy on even when skipSSRFCheck is set,
	// which takes the pre-flight check and the request interceptor out of the way
	// so the proxy's own refusal is what the test observes.
	forceEnforce bool
	// allowAuthority is the one host:port whose loopback addresses the proxy may
	// reach while every other rule stays enforced (empty blocks loopback
	// everywhere).
	allowAuthority string
}

// startBrowserProxy starts the loopback filtering proxy. A failure is returned as
// nil: the callers refuse the crawl instead of weakening the sandbox (issue #8).
//
// It is a variable so a test can prove that fail-closed policy without arranging a
// listen failure; production always uses this implementation.
var startBrowserProxy = func() *browserproxy.Proxy {
	enforce := !skipSSRFCheck || browserSandboxConfig.forceEnforce
	opts := []netsafe.Option{}
	if browserSandboxConfig.allowAuthority != "" {
		opts = append(opts, netsafe.AllowAuthority(browserSandboxConfig.allowAuthority))
	}
	proxy, err := browserproxy.Start(enforce, opts...)
	if err != nil {
		tlog.Warn("web.browser", "proxy_start_failed", "err", err.Error())
		return nil
	}
	return proxy
}

// appendBrowserProxyArgs appends the proxy flags to a Chromium argv.
func appendBrowserProxyArgs(args []string, proxyURL string) []string {
	valued, bare := browserProxyFlags(proxyURL)
	for _, kv := range valued {
		args = append(args, "--"+kv[0]+"="+kv[1])
	}
	for _, flag := range bare {
		args = append(args, "--"+flag)
	}
	return args
}

// ── Browser detection chain ──

// Browser commands to try in order for system-installed Chromium.
// (browserCommands is declared in web_extract.go)

// playwrightChromiumPath returns the path to a browser installed by Playwright,
// or "" when Playwright has no browser in its cache.
func playwrightChromiumPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return findPlaywrightBrowser(playwrightCacheDir(home, runtime.GOOS), runtime.GOOS)
}

// playwrightCacheDir is where Playwright unpacks the browsers it downloads.
func playwrightCacheDir(home, goos string) string {
	if goos == "darwin" {
		return filepath.Join(home, "Library", "Caches", "ms-playwright")
	}
	return filepath.Join(home, ".cache", "ms-playwright")
}

// findPlaywrightBrowser looks for a usable browser in a Playwright cache
// directory, newest revision first.
//
// Playwright has changed its layout more than once: the full browser now ships
// as "Google Chrome for Testing.app" under chrome-mac-arm64/chrome-mac-x64 (it
// used to be Chromium.app under chrome-mac), and there is a separate
// chrome_headless_shell package. Only looking for the old path made every
// current installation invisible, which silently fell back to rod downloading
// its own browser.
func findPlaywrightBrowser(cacheDir, goos string) string {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return ""
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
		case strings.HasPrefix(name, "chromium-"):
			pkgs = append(pkgs, pkg{name: name, rev: playwrightRevision(name, "chromium-")})
		case strings.HasPrefix(name, "chromium_headless_shell-"):
			pkgs = append(pkgs, pkg{name: name, rev: playwrightRevision(name, "chromium_headless_shell-"), shell: true})
		}
	}
	// Newest revision first, and the full browser before the headless shell: the
	// shell is a stripped build, so it is only a fallback. The revision is parsed
	// rather than compared as text, where "chromium-999" would outrank
	// "chromium-1000".
	sort.Slice(pkgs, func(i, j int) bool {
		if pkgs[i].shell != pkgs[j].shell {
			return !pkgs[i].shell
		}
		if pkgs[i].rev != pkgs[j].rev {
			return pkgs[i].rev > pkgs[j].rev
		}
		return pkgs[i].name > pkgs[j].name
	})

	for _, p := range pkgs {
		dir := p.name
		for _, rel := range playwrightBrowserRelPaths(dir, goos) {
			path := filepath.Join(cacheDir, rel)
			if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
				return path
			}
		}
	}
	return ""
}

// playwrightRevision extracts the numeric revision from a Playwright package
// directory name, or -1 when it cannot be parsed.
func playwrightRevision(name, prefix string) int {
	rev, err := strconv.Atoi(strings.TrimPrefix(name, prefix))
	if err != nil {
		return -1
	}
	return rev
}

// playwrightBrowserRelPaths lists the layouts to try inside one Playwright
// package directory, the full browser before the headless shell.
func playwrightBrowserRelPaths(dir, goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			filepath.Join(dir, "chrome-mac-arm64", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing"),
			filepath.Join(dir, "chrome-mac-x64", "Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing"),
			filepath.Join(dir, "chrome-mac", "Chromium.app", "Contents", "MacOS", "Chromium"),
			filepath.Join(dir, "chrome-headless-shell-mac-arm64", "chrome-headless-shell"),
			filepath.Join(dir, "chrome-headless-shell-mac-x64", "chrome-headless-shell"),
		}
	case "windows":
		return []string{
			filepath.Join(dir, "chrome-win", "chrome.exe"),
			filepath.Join(dir, "chrome-headless-shell-win64", "chrome-headless-shell.exe"),
		}
	default: // linux
		return []string{
			filepath.Join(dir, "chrome-linux64", "chrome"),
			filepath.Join(dir, "chrome-linux", "chrome"),
			filepath.Join(dir, "chrome-headless-shell-linux64", "chrome-headless-shell"),
		}
	}
}

// browserEnvOverrides are the environment variables that point the tools at a
// specific browser. They win over every guess below: browser-actions/setup-chrome
// publishes the binary it installed through its `chrome-path` output, so a
// workflow sets CHROME_PATH and discovery does not have to reason about PATH
// order or about which of several installed builds actually works.
var browserEnvOverrides = []string{"CHROME_PATH", "CHROME"}

// browserProbeTimeout bounds the `--version` probe. It is generous on purpose: a
// real browser answers in a fraction of a second, but a loaded runner can be
// much slower, and rejecting a working browser would be worse than waiting. A
// distro package whose launcher cannot reach its daemon (the snap shim on GitHub
// runners) does not fail fast at all - it hangs, which is how the browser smoke
// test spent 35 s inside the extractor before being killed - so the bound is what
// keeps that failure to one timeout per discovery burst (see
// browserProbeRetryAfter).
var browserProbeTimeout = 5 * time.Second

// browserProbeRetryAfter is how long a probe that ran out of budget is believed.
// A timeout is not a verdict about the binary - it says only that the machine did
// not answer in time - so it may not stand for the life of the process: one
// loaded moment would otherwise reject a working browser for every extraction
// that follows (issue #26). A cooldown holds both ends: a burst of extractions
// still pays one timeout instead of one per call, and a browser that was merely
// slow to start is discovered again half a minute later.
var browserProbeRetryAfter = 30 * time.Second

// browserProbeVerdict is a memoized probe answer. probedAt is the zero time for a
// verdict that stands for the life of the process - the binary answered, or it is
// missing, not executable, or exits non-zero - and is set for one taken when the
// budget expired, which is probed again once browserProbeRetryAfter has passed.
type browserProbeVerdict struct {
	usable   bool
	probedAt time.Time
}

var (
	browserProbeMu    sync.Mutex
	browserProbeCache = map[string]browserProbeVerdict{}
)

// browserUsable reports whether the binary answers `--version` within
// browserProbeTimeout. The answer is memoized because discovery runs on every
// extraction and the layout cannot change while the process lives - except for a
// timed-out probe, which expires.
func browserUsable(path string) bool {
	browserProbeMu.Lock()
	if v, hit := browserProbeCache[path]; hit &&
		(v.probedAt.IsZero() || time.Since(v.probedAt) < browserProbeRetryAfter) {
		browserProbeMu.Unlock()
		return v.usable
	}
	browserProbeMu.Unlock()

	usable, timedOut := probeBrowserVersion(path)

	verdict := browserProbeVerdict{usable: usable}
	if timedOut {
		verdict.probedAt = time.Now()
	}
	browserProbeMu.Lock()
	browserProbeCache[path] = verdict
	browserProbeMu.Unlock()
	return usable
}

// probeBrowserVersion runs `<path> --version` under browserProbeTimeout. The
// second result marks a probe that the budget ended rather than one that got an
// answer: CommandContext kills the process at the deadline, so a binary that was
// slow to start on a loaded machine looks exactly like the shim that never
// answers, and only the caller's retry policy can tell them apart.
func probeBrowserVersion(path string) (usable, timedOut bool) {
	ctx, cancel := context.WithTimeout(context.Background(), browserProbeTimeout)
	defer cancel()
	if err := exec.CommandContext(ctx, path, "--version").Run(); err == nil {
		return true, false
	}
	return false, errors.Is(ctx.Err(), context.DeadlineExceeded)
}

// browserCandidates returns the paths to try, in preference order: an explicit
// override, then the system commands, then the Playwright cache. Repeated paths
// are dropped so one binary is probed once.
func browserCandidates(getenv func(string) string, lookPath func(string) (string, error), playwright func() string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}

	for _, name := range browserEnvOverrides {
		add(getenv(name))
	}
	for _, name := range browserCommands {
		if path, err := lookPath(name); err == nil {
			add(path)
		}
	}
	add(playwright())
	return out
}

// firstUsableBrowser returns the first candidate the probe accepts, or "" when
// none is usable.
func firstUsableBrowser(candidates []string, probe func(string) bool) string {
	for _, path := range candidates {
		if probe(path) {
			return path
		}
	}
	return ""
}

// browserContainerFlags adds the flag Chromium needs where the kernel or the
// container denies it a user namespace - a GitHub runner, a CI container, most
// sandboxes. Without it Chromium aborts in the zygote with "No usable sandbox!"
// and never publishes a debug URL, which is how the rod half of the smoke test
// failed. (--disable-dev-shm-usage, the other container flag, is already in
// rod's default set and is pinned by a test rather than added here.)
//
// --no-sandbox is not new to the browser paths: crawlViaExec has always passed
// it. The enforced boundary for this browser is the validating proxy (every
// hostname is resolved, checked and pinned before Chromium sees it), not
// Chromium's own process sandbox, and the alternative here was a browser that
// cannot start at all.
func browserContainerFlags(l *launcher.Launcher) *launcher.Launcher {
	return l.Set(flags.NoSandbox)
}

// findBrowser returns the path to a usable Chromium/Chrome binary, or "" when
// none is installed. Every candidate is probed before it is returned, so a broken
// installation is skipped instead of being handed to a caller that then hangs;
// the rod path falls back to its own launcher when this returns "".
func findBrowser() string {
	return firstUsableBrowser(
		browserCandidates(os.Getenv, exec.LookPath, playwrightChromiumPath),
		browserUsable,
	)
}

// FindBrowser returns the path to a usable Chromium/Chrome binary, or "" when
// none is installed. It exposes the same discovery rules the web tools use to
// callers outside this package, such as the TUI screenshot harness, so a second
// copy of the system/Playwright search order cannot drift from this one.
func FindBrowser() string { return findBrowser() }

// execBrowserArgs builds the argument list for the --dump-dom path. It is pure so
// the flags can be asserted without launching a browser.
//
// Two flags carry findings from CI:
//
//   - --single-process is deliberately absent. It used to be added on Linux "for
//     headless server environments", but Chromium documents it as unsupported and
//     it aborts while rendering on the GitHub runner's Chromium
//     ("signal: aborted (core dumped)"), which is how the exec half of the smoke
//     test died.
//   - --user-data-dir points at the caller's throwaway directory, so extraction
//     never touches the user's real profile.
func execBrowserArgs(profileDir, url, proxyURL, hostRule string) []string {
	args := []string{
		"--headless",
		"--disable-gpu",
		"--no-sandbox",
		"--disable-breakpad",
		"--user-data-dir=" + profileDir,
	}
	// Linux-specific flag for headless server environments.
	if runtime.GOOS == "linux" {
		args = append(args, "--disable-dev-shm-usage")
	}
	// Pin the top-level host to the address this process validated, so Chromium
	// cannot be rebound to a private address by a second DNS answer. The exec
	// path cannot intercept requests at all, so this is its only protection for
	// the initial navigation.
	if hostRule != "" {
		args = append(args, "--host-resolver-rules="+hostRule)
	}
	if proxyURL != "" {
		args = appendBrowserProxyArgs(args, proxyURL)
	}
	return append(args, "--dump-dom", url)
}

// lastLines returns at most n non-empty lines from the end of s, with the whole
// result bounded so a chatty Chromium cannot flood an error message.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	kept := make([]string, 0, n)
	for i := len(lines) - 1; i >= 0 && len(kept) < n; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		kept = append([]string{line}, kept...)
	}
	out := strings.Join(kept, " | ")
	if len(out) > 500 {
		out = out[len(out)-500:]
	}
	return out
}

// crawlViaExec uses a Chromium binary with --dump-dom to extract page content.
func crawlViaExec(ctx context.Context, browserPath, url string) (string, error) {
	// Chromium performs its own DNS resolution, so validate the target with the
	// shared SSRF policy before the process is launched at all.
	if err := checkBrowserTarget(url); err != nil {
		return "", err
	}

	ctx2, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()

	// This path cannot intercept requests, so without the proxy only the
	// top-level URL was checked: subresources, redirect hops and JavaScript
	// fetches went straight from Chromium to the network. The proxy sees them
	// all because Chromium asks it for every hostname, and it resolves,
	// validates and pins each one in this process. There is no fallback: this
	// path without the proxy is not a sandboxed browser, so it does not run.
	proxy := startBrowserProxy()
	if proxy == nil {
		return "", errBrowserSandboxUnavailable
	}
	defer proxy.Close()
	proxyURL := proxy.URL()

	// A throwaway profile: without --user-data-dir Chromium reads and writes the
	// user's real Chrome profile, which is invasive, fails where that profile is
	// not writable ("Failed to create headless user data directory container"),
	// and can collide with a Chrome the user already has open.
	profileDir, err := os.MkdirTemp("", "tinycode-chrome-*")
	if err != nil {
		return "", fmt.Errorf("create a chrome profile dir: %w", err)
	}
	defer os.RemoveAll(profileDir)

	args := execBrowserArgs(profileDir, url, proxyURL, browserHostRule(url))
	cmd := exec.CommandContext(ctx2, browserPath, args...)
	output, err := cmd.Output()
	if err != nil {
		// Chromium only explains itself on stderr, and Output() puts that in the
		// ExitError. Without it a core dump in CI leaves no trace of the reason.
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("exec: %w: %s", err, lastLines(string(exit.Stderr), 3))
		}
		return "", fmt.Errorf("exec: %w", err)
	}
	if len(output) < 200 {
		return "", fmt.Errorf("page too short (%d bytes)", len(output))
	}
	return processContent(string(output)), nil
}

// browserRequestAllowed is the SSRF predicate applied to every request the
// headless browser makes, not just the top-level navigation. It returns nil for
// a public http(s) URL, for schemes that never touch the network (inline
// `data:`/`blob:` subresources, which pages use heavily) and whenever the
// package-wide skipSSRFCheck test hook is set.
//
// It is deliberately resolution-only: it performs no HTTP request, so it is safe
// to call from inside the browser's request-interception handler, where a nested
// fetch could deadlock or recurse back into the interceptor.
func browserRequestAllowed(rawURL string) error {
	if skipSSRFCheck {
		return nil
	}
	if scheme := urlScheme(rawURL); nonNetworkScheme(scheme) {
		return nil
	}
	return checkSSRF(rawURL)
}

// nonNetworkScheme reports whether a URL scheme is resolved locally by the
// browser and therefore cannot be used to reach a network service. `file:` and
// the network-capable schemes are deliberately NOT in this set.
func nonNetworkScheme(scheme string) bool {
	switch scheme {
	case "data", "blob", "about", "chrome", "devtools":
		return true
	}
	return false
}

// urlScheme returns the lower-cased scheme of rawURL, or "" when it has none.
func urlScheme(rawURL string) string {
	i := strings.Index(rawURL, ":")
	if i <= 0 {
		return ""
	}
	scheme := rawURL[:i]
	for _, r := range scheme {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '+' || r == '-' || r == '.') {
			return ""
		}
	}
	return strings.ToLower(scheme)
}

// hijackDecision is the outcome of applying the SSRF policy to one intercepted
// request: let it reach the network, or fail it before any bytes are sent.
type hijackDecision int

const (
	// hijackContinue forwards the request to its real destination unchanged.
	hijackContinue hijackDecision = iota
	// hijackAbort fails the request without contacting the target.
	hijackAbort
)

// browserHijackDecisionFor maps the SSRF decision for a request URL onto the
// action the rod interceptor must take. It is a pure function of the URL (plus
// the SSRF lookup) so the policy is unit-testable without a browser.
func browserHijackDecisionFor(rawURL string) hijackDecision {
	if browserRequestAllowed(rawURL) == nil {
		return hijackContinue
	}
	return hijackAbort
}

// interceptBrowserRequest is the rod request-interception handler. Chromium
// resolves DNS, follows redirects and loads subresources itself, so the
// pre-flight checkBrowserTarget call cannot see them; this handler runs for
// every request the browser makes (documents, 3xx hops, JS/meta redirects,
// XHR/fetch, frames, images and other subresources) and only public http(s)
// targets are continued.
//
// Both branches must be explicit: a handler that neither continues nor fails a
// request makes rod fulfil it with a synthetic 200 response.
func interceptBrowserRequest(h *rod.Hijack) {
	// rod invokes handlers from its own dispatcher goroutine, where the
	// recover() in crawlViaRod cannot reach. A panic here would kill the whole
	// agent process, so fail the request closed instead.
	defer func() {
		if r := recover(); r != nil {
			h.Response.Fail(proto.NetworkErrorReasonAborted)
		}
	}()

	if browserHijackDecisionFor(h.Request.URL().String()) == hijackAbort {
		// Fail (not MustFail) so a protocol error cannot panic this goroutine.
		h.Response.Fail(proto.NetworkErrorReasonAborted)
		return
	}
	h.ContinueRequest(&proto.FetchContinueRequest{})
}

// crawlViaRod uses go-rod to render the page (auto-downloads Chromium if needed).
// Every rod call uses its error-returning form: a panic inside a tool goroutine
// would terminate the whole agent process.
func crawlViaRod(ctx context.Context, url string) (content string, err error) {
	// Chromium performs its own DNS resolution, so validate the target with the
	// shared SSRF policy before the browser is launched at all.
	if err := checkBrowserTarget(url); err != nil {
		return "", err
	}

	// Defensive recovery: rod's control loop can still panic on protocol
	// errors, and a panic in a tool goroutine would kill the process.
	defer func() {
		if r := recover(); r != nil {
			content = ""
			err = fmt.Errorf("rod: recovered from panic: %v", r)
		}
	}()

	// Route the browser through the loopback filtering proxy: every hostname it
	// contacts — redirect hops, subresources, XHR/fetch, frames, and the CONNECT
	// tunnels the interceptor below cannot see into — is resolved, validated and
	// pinned by this process instead of by Chromium. It is required, not best
	// effort (issue #8): a browser outside the sandbox is not the fallback this
	// path is for, so the crawl fails instead of downgrading silently.
	proxy := startBrowserProxy()
	if proxy == nil {
		return "", errBrowserSandboxUnavailable
	}
	defer proxy.Close()

	// Build the launcher explicitly rather than letting rod create one, so the
	// proxy flags reach Chromium in every case; the top-level host is pinned to
	// the address this process validated as well.
	//
	// Append takes a flag name and its values; rod renders --name=value as a
	// single argv element. The rest (headless, random debugging port, the
	// caller's context so the browser is killed on cancel) is what rod's default
	// launcher does as well.
	l := launcher.New().Headless(true).Context(ctx)
	// Use an installed browser rather than letting rod download one: findBrowser
	// knows the system browsers and the Playwright cache, and downloading on the
	// first crawl is slow and fails on a network-restricted host.
	if bin := findBrowser(); bin != "" {
		l = l.Bin(bin)
	}
	l = browserContainerFlags(l)
	if rule := browserHostRule(url); rule != "" {
		l = l.Append("host-resolver-rules", rule)
	}
	valued, bare := browserProxyFlags(proxy.URL())
	for _, kv := range valued {
		l = l.Append(flags.Flag(kv[0]), kv[1])
	}
	for _, name := range bare {
		l = l.Append(flags.Flag(name))
	}

	wsURL, launchErr := l.Launch()
	if launchErr != nil {
		// No retry without the sandbox flags. That retry was how a launcher
		// failure turned into a browser with no proxy, no pinning and no
		// non-proxied-channel hardening — a downgrade the caller could not see.
		// The error is reported instead (issue #8).
		l.Cleanup()
		tlog.Warn("web.browser", "launch_flags_failed", "url", url, "err", launchErr.Error())
		return "", fmt.Errorf("%w: launching the browser with the sandbox flags failed: %v",
			errBrowserSandboxUnavailable, launchErr)
	}
	defer l.Cleanup()
	browser := rod.New().ControlURL(wsURL).Context(ctx)

	if err := browser.Connect(); err != nil {
		return "", fmt.Errorf("connect browser: %w", err)
	}
	defer browser.Close()

	// Enforce the SSRF policy inside Chromium. The pre-flight checkBrowserTarget
	// call above only sees the top-level URL and its observable HTTP redirect
	// chain; the interceptor covers everything Chromium requests afterwards,
	// including page-level redirects and subresources. Registering it before the
	// page is created means the initial navigation is intercepted too, and it is
	// continued because checkBrowserTarget already validated that URL.
	//
	// Browser.HijackRequests (not Page.HijackRequests) covers the whole browser,
	// so targets opened by the page cannot slip past the policy.
	router := browser.HijackRequests()
	if err := router.Add("*", "", interceptBrowserRequest); err != nil {
		return "", fmt.Errorf("install request interceptor: %w", err)
	}
	defer func() { _ = router.Stop() }()
	go router.Run()

	page, err := browser.Page(proto.TargetCreateTarget{URL: url})
	if err != nil {
		return "", fmt.Errorf("open page %s: %w", url, err)
	}
	defer page.Close()

	if err := page.WaitLoad(); err != nil {
		return "", fmt.Errorf("wait for page load: %w", err)
	}

	// Scroll to trigger lazy-loaded content. rod's Eval sends the string as a
	// function declaration and calls it, so a bare expression like
	// `window.scrollTo(...)` becomes `(...).apply(...)` on undefined and throws
	// "apply is not a function" - every expression here has to be a function.
	if _, err := page.Eval(`() => window.scrollTo(0, document.body.scrollHeight)`); err != nil {
		return "", fmt.Errorf("scroll to bottom: %w", err)
	}
	time.Sleep(time.Second)
	if _, err := page.Eval(`() => window.scrollTo(0, 0)`); err != nil {
		return "", fmt.Errorf("scroll to top: %w", err)
	}

	// Extract title and content.
	titleObj, err := page.Eval(`() => document.title`)
	if err != nil {
		return "", fmt.Errorf("read document title: %w", err)
	}
	contentObj, err := page.Eval(`() => document.body.innerText`)
	if err != nil {
		return "", fmt.Errorf("read document body: %w", err)
	}

	title := titleObj.Value.Str()
	body := contentObj.Value.Str()
	if body == "" {
		return "", fmt.Errorf("no content extracted from %s", url)
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n\n_Source: %s_\n\n---\n\n%s", title, url, body))
	return sb.String(), nil
}

// WebExtractBrowser returns a Tool that crawls web pages using a headless
// browser with JavaScript rendering support (handles SPA, SSR, anti-bot pages).
func WebExtractBrowser() Tool {
	return Tool{
		Name:        "web_extract_browser",
		Description: "Extract content from JS-rendered web pages using a headless browser. Handles SPAs, lazy loading, and Cloudflare-protected sites. Falls back to auto-downloaded Chromium if none is installed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"url": map[string]any{
					"type":        "string",
					"description": "The URL to crawl",
				},
				"mode": map[string]any{
					"type":        "string",
					"enum":        []any{"auto", "full"},
					"description": "'auto' extracts main article content (default), 'full' returns all page text",
				},
			},
			"required": []string{"url"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			url, _ := args["url"].(string)
			if url == "" {
				return "", fmt.Errorf("url is required")
			}
			// Block non-public targets before any Chromium process is spawned.
			if err := checkBrowserTarget(url); err != nil {
				return "", err
			}
			mode, _ := args["mode"].(string)
			if mode == "" {
				mode = "auto"
			}

			// Try system/playwright Chromium first
			if browserPath := findBrowser(); browserPath != "" {
				content, err := crawlViaExec(ctx, browserPath, url)
				if err == nil {
					if mode == "full" {
						return content, nil
					}
					// Extract article content
					return extractArticle(mode, content, url), nil
				}
			}

			// Fallback to go-rod (auto-downloads Chromium)
			content, err := crawlViaRod(ctx, url)
			if err != nil {
				return "", fmt.Errorf("all browser methods failed: %w", err)
			}
			return content, nil
		},
	}
}

// extractArticle formats the content into a clean Markdown document.
func extractArticle(mode, content, url string) string {
	// Extract title from first <h1> or --- delimited line
	title := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			title = strings.TrimPrefix(trimmed, "# ")
			break
		}
	}
	if title == "" {
		title = url
	}

	// Find first paragraph (non-empty text block) as description
	desc := ""
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if len(trimmed) > 30 && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, "---") {
			if len(trimmed) > 150 {
				desc = trimmed[:147] + "..."
			} else {
				desc = trimmed
			}
			break
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("# %s\n", title))
	if desc != "" {
		sb.WriteString(fmt.Sprintf("\n> %s\n", desc))
	}
	sb.WriteString(fmt.Sprintf("\n_Source: %s_\n\n---\n\n", url))
	sb.WriteString(content)
	if len(sb.String()) > 5000 {
		return sb.String()[:5000] + "\n\n[... content truncated at 5000 chars ...]"
	}
	return sb.String()
}
