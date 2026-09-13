package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"

	"github.com/yusiwen/tinycode/tlog"
)

// ── Browser detection chain ──

// Browser commands to try in order for system-installed Chromium.
// (browserCommands is declared in web_extract.go)

// playwrightChromiumPath returns the path to a Chromium installed by Playwright.
func playwrightChromiumPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Platform-specific Playwright cache directory
	var playwrightDir string
	switch runtime.GOOS {
	case "darwin":
		playwrightDir = filepath.Join(home, "Library", "Caches", "ms-playwright")
	default:
		playwrightDir = filepath.Join(home, ".cache", "ms-playwright")
	}
	entries, err := os.ReadDir(playwrightDir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "chromium-") {
			// Platform-specific Chrome binary path within Playwright
			var sub string
			switch runtime.GOOS {
			case "darwin":
				sub = filepath.Join(playwrightDir, e.Name(), "chrome-mac", "Chromium.app", "Contents", "MacOS", "Chromium")
			default:
				sub = filepath.Join(playwrightDir, e.Name(), "chrome-linux", "chrome")
			}
			if fi, err := os.Stat(sub); err == nil && fi.Mode().IsRegular() {
				return sub
			}
		}
	}
	return ""
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

	// Pin the top-level host to the address we validated so the browser cannot
	// be rebound to a private address. Only the top-level host can be pinned
	// here (other names appear while the page loads); those are handled by the
	// request interceptor below. When no rule applies (IP literal, checks
	// skipped) the default launcher is used.
	var browser *rod.Browser
	if rule := browserHostRule(url); rule != "" {
		// Append takes the flag name and its values; rod renders it as
		// --host-resolver-rules=MAP host ip in a single argv element.
		// Same launcher rod would build by default (headless, leakless, random
		// debugging port), plus the pinning rule and the caller's context so the
		// browser is killed when the call is cancelled.
		l := launcher.New().Headless(true).Context(ctx).Append("host-resolver-rules", rule)
		wsURL, launchErr := l.Launch()
		if launchErr != nil {
			// Pinning is best effort: fall back to the default launcher rather
			// than losing the browser fallback entirely.
			l.Cleanup()
			tlog.Warn("web.browser", "pin_host_failed", "url", url, "err", launchErr.Error())
			browser = rod.New().Context(ctx)
		} else {
			defer l.Cleanup()
			browser = rod.New().ControlURL(wsURL).Context(ctx)
		}
	} else {
		browser = rod.New().Context(ctx)
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
