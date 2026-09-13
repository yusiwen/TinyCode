package tool

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/net/html"
)

var skipSSRFCheck = false

// browserCommands lists possible Chromium/Chrome binary names to try.
var browserCommands = []string{
	"chromium-browser",
	"chromium",
	"google-chrome",
	"google-chrome-stable",
	"chrome",
}

// Summarizer is a callback...
// Set via SetSummarizer. When non-nil, content >5000 chars is summarized.
var summarizer func(ctx context.Context, content string) (string, error)

// SetSummarizer configures an optional LLM-based summarizer for web_extract.
// The function receives extracted page content and should return a concise summary.
func SetSummarizer(fn func(ctx context.Context, content string) (string, error)) {
	summarizer = fn
}

func WebExtract() Tool {
	return Tool{
		Name: "web_extract",
		Description: "Fetch and extract content from web page URLs. Returns page content in markdown format. " +
			"Max 5 URLs per call. Fallback chain: direct HTTP → Cloudflare retry → Google Cache → Wayback Machine.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"urls": map[string]any{
					"type":        "array",
					"description": "List of URLs to extract content from (max 5 per call)",
					"items": map[string]any{
						"type": "string",
					},
				},
			},
			"required": []string{"urls"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			raw, ok := args["urls"]
			if !ok {
				return "", fmt.Errorf("urls is required")
			}
			urlsRaw, ok := raw.([]any)
			if !ok {
				return "", fmt.Errorf("urls must be an array")
			}
			if len(urlsRaw) == 0 {
				return "", fmt.Errorf("at least one URL is required")
			}
			if len(urlsRaw) > 5 {
				urlsRaw = urlsRaw[:5]
			}

			var results []string
			for _, u := range urlsRaw {
				urlStr, ok := u.(string)
				if !ok || urlStr == "" {
					continue
				}
				content, err := extractURL(ctx, urlStr)
				if err != nil {
					results = append(results, fmt.Sprintf("=== %s ===\nError: %s", urlStr, err))
				} else if len(strings.TrimSpace(content)) < 20 {
					results = append(results, fmt.Sprintf("=== %s ===\nNo content extracted.", urlStr))
				} else {
					if len(content) > 5000 {
						if summarizer != nil {
							summary, err := summarizer(ctx, content)
							if err == nil && len(summary) > 0 && len(summary) < len(content) {
								content = summary + "\n\n[... LLM-summarized from longer content ...]"
							} else {
								content = content[:5000] + "\n\n[... content truncated at 5000 chars ...]"
							}
						} else {
							content = content[:5000] + "\n\n[... content truncated at 5000 chars ...]"
						}
					}
					results = append(results, fmt.Sprintf("=== %s ===\n%s", urlStr, content))
				}
			}

			return strings.Join(results, "\n\n"), nil
		},
	}
}

func extractURL(ctx context.Context, urlStr string) (string, error) {
	if !skipSSRFCheck {
		if err := checkSSRF(urlStr); err != nil {
			return "", err
		}
	}

	// 1. Direct HTTP
	content, status, cfBlocked, err := fetchURL(ctx, urlStr, "Mozilla/5.0 (compatible; TinyCode/1.0)")
	if err == nil && status == 200 {
		return processContent(content), nil
	}

	// 2. Cloudflare bypass
	if cfBlocked {
		content, status, _, err = fetchURL(ctx, urlStr, "opencode (+https://github.com/opencode-ai)")
		if err == nil && status == 200 {
			return processContent(content), nil
		}
	}

	// 3. Google Cache
	cacheURL := fmt.Sprintf("https://webcache.googleusercontent.com/search?q=cache:%s",
		url.QueryEscape(urlStr))
	content, _, _, err = fetchURL(ctx, cacheURL, "Mozilla/5.0 (compatible; TinyCode/1.0)")
	if err == nil && len(content) > 200 {
		if processed := processContent(content); len(processed) > 50 {
			return processed + "\n\n(来源: Google Cache)", nil
		}
	}

	// 4. Wayback Machine
	if snapContent := tryWayback(ctx, urlStr); snapContent != "" {
		return snapContent + "\n\n(来源: Wayback Machine)", nil
	}

	// 5. Browser rendering (local Chromium) for JS-heavy pages
	if browserContent := tryBrowser(ctx, urlStr); browserContent != "" {
		return browserContent + "\n\n(来源: Chromium headless)", nil
	}

	if err != nil {
		return "", fmt.Errorf("all fetch methods failed: %w", err)
	}
	return "", fmt.Errorf("all fetch methods failed (status %d)", status)
}

func fetchURL(ctx context.Context, urlStr, userAgent string) (string, int, bool, error) {
	client := newSSRFProtectedClient(30 * time.Second)
	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return "", 0, false, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, false, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	bodyStr := string(data)
	isCF := resp.StatusCode == 403 && strings.Contains(resp.Header.Get("Server"), "cloudflare")

	if resp.StatusCode != 200 {
		return bodyStr, resp.StatusCode, isCF, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return bodyStr, 200, false, nil
}

var minContentLen = 10

func processContent(content string) string {
	contentType := sniffContentType(content)
	if contentType != "html" {
		return content
	}
	out, err := htmlToMarkdown(content)
	if err != nil {
		return content
	}
	out = strings.TrimSpace(out)
	if len(out) < 10 {
		return ""
	}
	if len(out) > 5000 {
		out = out[:5000] + "\n\n[... content truncated at 5000 chars ...]"
	}
	return out
}

func sniffContentType(body string) string {
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "<!DOCTYPE") || strings.HasPrefix(trimmed, "<html") ||
		strings.HasPrefix(trimmed, "<!doctype") {
		return "html"
	}
	return "raw"
}

func tryWayback(ctx context.Context, urlStr string) string {
	cdxURL := fmt.Sprintf("https://web.archive.org/cdx/search/cdx?url=%s&output=json&limit=3",
		url.QueryEscape(urlStr))
	client := newSSRFProtectedClient(15 * time.Second)
	req, err := http.NewRequestWithContext(ctx, "GET", cdxURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; TinyCode/1.0)")
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return ""
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 65536))
	lines := strings.Split(string(body), "\n")
	for i := 1; i < len(lines); i++ {
		fields := strings.Split(lines[i], " ")
		if len(fields) < 2 {
			continue
		}
		ts := fields[1]
		wbURL := fmt.Sprintf("https://web.archive.org/web/%sid_/%s", ts, urlStr)
		wbContent, _, _, wbErr := fetchURL(ctx, wbURL, "Mozilla/5.0 (compatible; TinyCode/1.0)")
		if wbErr == nil && len(wbContent) > 200 {
			return processContent(wbContent)
		}
	}
	return ""
}

// maxSSRFRedirects caps how many HTTP redirect hops the hardened client follows.
const maxSSRFRedirects = 5

// newSSRFProtectedClient returns an *http.Client hardened against SSRF.
//
// The target host is resolved exactly once per connection, every resolved IP is
// validated against the shared SSRF policy, and the connection is then pinned to
// that already-validated IP. A DNS rebinding attacker therefore cannot swap the
// destination between the check and the connect. Every redirect target is
// re-validated with the same policy and the hop count is capped.
func newSSRFProtectedClient(timeout time.Duration) *http.Client {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			// A proxy resolves the hostname itself, which would defeat the
			// pinned-IP guarantee enforced by DialContext below.
			Proxy:                 nil,
			DialContext:           ssrfDialContext(dialer),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          16,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: ssrfRedirectPolicy(maxSSRFRedirects),
	}
}

// ssrfDialContext resolves the host once, validates every resolved IP, and dials
// only an address that already passed validation.
func ssrfDialContext(dialer *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("split host port %q: %w", addr, err)
		}
		ips, err := resolveValidatedHost(ctx, host, !skipSSRFCheck)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no usable address for %q", host)
		}
		return nil, fmt.Errorf("dial %q: %w", host, lastErr)
	}
}

// ssrfRedirectPolicy returns a CheckRedirect hook that re-validates every
// redirect target with the shared SSRF policy and refuses extra hops.
func ssrfRedirectPolicy(maxHops int) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxHops {
			return fmt.Errorf("stopped after %d redirects", maxHops)
		}
		if skipSSRFCheck {
			return nil
		}
		if err := checkSSRF(req.URL.String()); err != nil {
			return fmt.Errorf("redirect blocked: %w", err)
		}
		return nil
	}
}

// browserPreflightTimeout bounds the redirect-chain probe run before a
// headless browser is launched.
const browserPreflightTimeout = 10 * time.Second

// checkBrowserTarget validates a URL before it is handed to a headless
// Chromium process. Chromium performs its own DNS resolution and follows
// redirects itself, so in addition to validating the address we walk the HTTP
// redirect chain with the SSRF-protected client: a chain that leaves the public
// internet is refused before the browser starts.
//
// This is only the pre-flight check. Requests that only the browser can observe
// (HTTP 3xx hops, JavaScript/meta-refresh redirects, XHR/fetch, frames and other
// subresources) are enforced in-browser by the request interceptor wired up in
// crawlViaRod, which applies the same policy through browserRequestAllowed. The
// --dump-dom paths (crawlViaExec, tryBrowser) cannot intercept requests and stay
// limited to this pre-flight check.
func checkBrowserTarget(rawURL string) error {
	if skipSSRFCheck {
		return nil
	}
	if err := checkSSRF(rawURL); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil // let the browser path report the malformed URL
	}
	// Ask for a single byte: the response body is irrelevant, only the
	// redirect chain is being validated.
	req.Header.Set("Range", "bytes=0-0")

	resp, err := newSSRFProtectedClient(browserPreflightTimeout).Do(req)
	if err != nil {
		if strings.Contains(err.Error(), "redirect blocked") {
			return fmt.Errorf("browser target refused: %w", err)
		}
		// Any other failure (DNS, TLS, connection) is reported by the browser
		// path itself; do not mask it here.
		return nil
	}
	resp.Body.Close()
	return nil
}

// resolveValidatedHost resolves host exactly once. When enforce is true every
// resolved address must pass the SSRF policy, so a hostname that mixes public
// and private addresses is rejected instead of silently dialed.
func resolveValidatedHost(ctx context.Context, host string, enforce bool) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if enforce {
			if err := validatePublicIP(ip, host); err != nil {
				return nil, err
			}
		}
		return []net.IP{ip}, nil
	}

	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("SSRF: DNS resolution failed for %q: %w", host, err)
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if enforce {
			if err := validatePublicIP(addr.IP, host); err != nil {
				return nil, err
			}
		}
		ips = append(ips, addr.IP)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("SSRF: no addresses resolved for host %q", host)
	}
	return ips, nil
}

// validatePublicIP rejects any address that is not a globally routable unicast IP.
func validatePublicIP(ip net.IP, host string) error {
	if isPrivateIP(ip) {
		return fmt.Errorf("SSRF: blocked non-public IP %q for host %q", ip.String(), host)
	}
	return nil
}

// blockedHosts lists cloud metadata endpoints that must never be fetched.
var blockedHosts = map[string]bool{
	"169.254.169.254":          true,
	"169.254.170.2":            true,
	"169.254.169.253":          true,
	"metadata.google.internal": true,
	"metadata.goog":            true,
	"100.100.100.200":          true,
}

// checkSSRF validates a URL against the SSRF policy. Cloud metadata hostnames
// and any host resolving to a private, loopback, link-local, unspecified,
// CGNAT, or multicast address are rejected.
func checkSSRF(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("SSRF: unsupported URL scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("SSRF: missing host in %q", rawURL)
	}
	if blockedHosts[strings.ToLower(host)] {
		return fmt.Errorf("SSRF: blocked host %q (cloud metadata)", host)
	}
	if _, err := resolveValidatedHost(context.Background(), host, true); err != nil {
		return err
	}
	return nil
}

// isPrivateIP reports whether ip is not a globally routable unicast address.
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4 and IPv4-mapped IPv6 addresses are checked explicitly so the
	// special ranges below cannot be smuggled in as ::ffff:a.b.c.d.
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 0: // 0.0.0.0/8 "this network" (includes 0.0.0.0)
			return true
		case ip4[0] == 10: // 10.0.0.0/8
			return true
		case ip4[0] == 127: // 127.0.0.0/8 loopback
			return true
		case ip4[0] == 169 && ip4[1] == 254: // 169.254.0.0/16 link-local / metadata
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31: // 172.16.0.0/12
			return true
		case ip4[0] == 192 && ip4[1] == 168: // 192.168.0.0/16
			return true
		case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127: // 100.64.0.0/10 CGNAT
			return true
		// Note: 198.18.0.0/15 is deliberately NOT blocked. Sandboxes and
		// transparent egress proxies commonly map public names into that
		// benchmarking range, so blocking it breaks legitimate fetches.
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0: // 192.0.0.0/24 IETF protocol assignments
			return true
		case ip4[0] >= 240: // 240.0.0.0/4 reserved, includes 255.255.255.255
			return true
		}
	}

	// IPv4-compatible IPv6 (::a.b.c.d) is not covered by To4, and NAT64
	// (64:ff9b::/96) embeds an IPv4 address; both could smuggle a loopback or
	// private target past the checks above.
	if ip16 := ip.To16(); ip16 != nil {
		allZero := true
		for _, b := range ip16[:12] {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return true
		}
		if ip16[0] == 0x00 && ip16[1] == 0x64 && ip16[2] == 0xff && ip16[3] == 0x9b {
			return true
		}
	}

	// Covers IPv6 loopback, link-local, unique-local, unspecified (::),
	// multicast, and link-local multicast ranges; !IsGlobalUnicast also rejects
	// the IPv4/6 broadcast and other non-unicast forms.
	return !ip.IsGlobalUnicast() ||
		ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsPrivate()
}

// ── HTML to Markdown converter ──
func htmlToMarkdown(htmlContent string) (string, error) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return "", fmt.Errorf("parse html: %w", err)
	}

	contentNode := findContentNode(doc)
	if contentNode == nil {
		contentNode = doc
	}

	var sb strings.Builder
	renderNode(contentNode, &sb, 0)
	return strings.TrimSpace(sb.String()), nil
}

func findContentNode(n *html.Node) *html.Node {
	var article, main, body *html.Node
	var search func(*html.Node)
	search = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "article":
				if article == nil {
					article = n
				}
			case "main":
				if main == nil {
					main = n
				}
			case "body":
				if body == nil {
					body = n
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			search(c)
		}
	}
	search(n)

	if article != nil {
		return article
	}
	if main != nil {
		return main
	}
	return body
}

func renderNode(n *html.Node, sb *strings.Builder, depth int) {
	switch n.Type {
	case html.TextNode:
		text := strings.TrimSpace(n.Data)
		if text != "" {
			sb.WriteString(text)
		}
	case html.ElementNode:
		switch n.Data {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			level := int(n.Data[1] - '0')
			sb.WriteString("\n\n")
			for i := 0; i < level; i++ {
				sb.WriteByte('#')
			}
			sb.WriteByte(' ')
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, depth+1)
			}
			sb.WriteString("\n")
		case "p":
			sb.WriteString("\n\n")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, depth+1)
			}
		case "br":
			sb.WriteString("\n")
		case "a":
			href := ""
			for _, attr := range n.Attr {
				if attr.Key == "href" {
					href = attr.Val
				}
			}
			var textSB strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, &textSB, depth+1)
			}
			text := strings.TrimSpace(textSB.String())
			if text != "" && href != "" && text != href {
				sb.WriteString(fmt.Sprintf("[%s](%s)", text, href))
			} else if text != "" {
				sb.WriteString(text)
			} else if href != "" {
				sb.WriteString(href)
			}
		case "strong", "b":
			sb.WriteString("**")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, depth+1)
			}
			sb.WriteString("**")
		case "em", "i":
			sb.WriteString("*")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, depth+1)
			}
			sb.WriteString("*")
		case "code":
			isInline := n.Parent == nil || n.Parent.Data != "pre"
			if isInline {
				sb.WriteString("`")
			} else {
				sb.WriteString("\n\n```\n")
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, depth+1)
			}
			if isInline {
				sb.WriteString("`")
			} else {
				sb.WriteString("\n```\n")
			}
		case "pre":
			sb.WriteString("\n\n")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, depth+1)
			}
			sb.WriteString("\n")
		case "ul", "ol":
			sb.WriteString("\n\n")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, depth+1)
			}
		case "li":
			sb.WriteString("\n- ")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, depth+1)
			}
		case "blockquote":
			sb.WriteString("\n\n> ")
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, depth+1)
			}
			sb.WriteString("\n")
		case "hr":
			sb.WriteString("\n\n---\n")
		case "img":
			alt, src := "", ""
			for _, attr := range n.Attr {
				switch attr.Key {
				case "alt":
					alt = attr.Val
				case "src":
					src = attr.Val
				}
			}
			if alt != "" {
				sb.WriteString(fmt.Sprintf("![%s](%s)", alt, src))
			} else if src != "" {
				sb.WriteString(fmt.Sprintf("![image](%s)", src))
			}
		case "script", "style", "nav", "footer", "header", "aside":
			return
		default:
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, depth+1)
			}
		}
	}
	if n.Type == html.ElementNode {
		switch n.Data {
		case "td", "th":
			sb.WriteString("  ")
		case "li":
			sb.WriteString("\n")
		}
	}
}

// tryBrowser attempts to fetch page content using a headless Chromium browser.
// Returns empty string if no browser is available or rendering fails.
func tryBrowser(ctx context.Context, urlStr string) string {
	// Never hand a non-public URL to Chromium.
	if err := checkBrowserTarget(urlStr); err != nil {
		return ""
	}

	// Find available browser
	browserPath := ""
	for _, name := range browserCommands {
		if path, err := exec.LookPath(name); err == nil {
			browserPath = path
			break
		}
	}
	if browserPath == "" {
		return ""
	}

	ctx2, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx2, browserPath,
		"--headless",
		"--disable-gpu",
		"--no-sandbox",
		"--dump-dom",
		urlStr,
	)
	cmd.Stderr = nil
	output, err := cmd.Output()
	if err != nil || len(output) < 200 {
		return ""
	}

	return processContent(string(output))
}
