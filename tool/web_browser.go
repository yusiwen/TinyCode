package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"

	"github.com/yusiwen/tinycode/internal/browserproxy"
	"github.com/yusiwen/tinycode/tlog"
)

// browserProxyFlags returns the Chromium flags that route the browser through
// the loopback filtering proxy, as flag/value pairs plus bare switches.
//
// Chromium bypasses any proxy for loopback addresses by default, and the proxy
// is precisely what stands between the browser and an internal service, so that
// shortcut is disabled with the magic "<-loopback>" entry. QUIC/HTTP3 travels
// over UDP and would leave without ever asking the proxy, so it is turned off
// as well.
func browserProxyFlags(proxyURL string) (valued [][2]string, bare []string) {
	return [][2]string{
		{"proxy-server", proxyURL},
		{"proxy-bypass-list", "<-loopback>"},
	}, []string{"disable-quic"}
}

// startBrowserProxy starts the loopback filtering proxy unless the package-wide
// SSRF hook is set. A failure is reported to the caller and is not fatal: the
// callers keep their other protections (the pre-flight check, the top-level host
// pin and the rod request interceptor) and log that the proxy is missing.
func startBrowserProxy() *browserproxy.Proxy {
	proxy, err := browserproxy.Start(!skipSSRFCheck)
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

// findBrowser returns the path to a usable Chromium/Chrome binary,
// or tries rod's auto-download as the final fallback.
// Returns empty string if nothing can be found.
func findBrowser() string {
	// 1. System-installed browsers
	for _, name := range browserCommands {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	// 2. Playwright-bundled Chromium
	if pw := playwrightChromiumPath(); pw != "" {
		return pw
	}
	return ""
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
	// validates and pins each one in this process.
	proxy := startBrowserProxy()
	if proxy != nil {
		defer proxy.Close()
	}

	args := []string{
		"--headless",
		"--disable-gpu",
		"--no-sandbox",
	}
	// Linux-specific flags for headless server environments
	if runtime.GOOS == "linux" {
		args = append(args, "--disable-dev-shm-usage", "--single-process")
	}
	// Pin the top-level host to the address this process validated, so Chromium
	// cannot be rebound to a private address by a second DNS answer. The exec
	// path cannot intercept requests at all, so this is its only protection for
	// the initial navigation.
	if rule := browserHostRule(url); rule != "" {
		args = append(args, "--host-resolver-rules="+rule)
	}
	if proxy != nil {
		args = appendBrowserProxyArgs(args, proxy.URL())
	}
	args = append(args, "--dump-dom", url)
	cmd := exec.CommandContext(ctx2, browserPath, args...)
	output, err := cmd.Output()
	if err != nil {
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
	// contacts — redirect hops, subresources, XHR/fetch, frames — is resolved,
	// validated and pinned by this process instead of by Chromium.
	proxy := startBrowserProxy()
	if proxy != nil {
		defer proxy.Close()
	}

	// Build the launcher explicitly rather than letting rod create one, so the
	// proxy flags reach Chromium in every case. On top of that the top-level host
	// is pinned to the address this process validated, which covers the case
	// where the proxy could not be started.
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
	if rule := browserHostRule(url); rule != "" {
		l = l.Append("host-resolver-rules", rule)
	}
	if proxy != nil {
		valued, bare := browserProxyFlags(proxy.URL())
		for _, kv := range valued {
			l = l.Append(flags.Flag(kv[0]), kv[1])
		}
		for _, name := range bare {
			l = l.Append(flags.Flag(name))
		}
	}

	var browser *rod.Browser
	wsURL, launchErr := l.Launch()
	if launchErr != nil {
		// Both protections are best effort here: fall back to rod's own launcher
		// rather than losing the browser fallback entirely. The pre-flight check
		// and the request interceptor installed below still apply.
		l.Cleanup()
		tlog.Warn("web.browser", "launch_flags_failed", "url", url, "err", launchErr.Error())
		browser = rod.New().Context(ctx)
	} else {
		defer l.Cleanup()
		browser = rod.New().ControlURL(wsURL).Context(ctx)
	}
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

	// Scroll to trigger lazy-loaded content.
	if _, err := page.Eval(`window.scrollTo(0, document.body.scrollHeight)`); err != nil {
		return "", fmt.Errorf("scroll to bottom: %w", err)
	}
	if err := page.Wait(rod.Eval("1s")); err != nil {
		return "", fmt.Errorf("wait after scroll: %w", err)
	}
	if _, err := page.Eval(`window.scrollTo(0, 0)`); err != nil {
		return "", fmt.Errorf("scroll to top: %w", err)
	}

	// Extract title and content.
	titleObj, err := page.Eval(`document.title`)
	if err != nil {
		return "", fmt.Errorf("read document title: %w", err)
	}
	contentObj, err := page.Eval(`document.body.innerText`)
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
