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

	"github.com/yusiwen/tinycode/internal/netsafe"
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

// minContentLen is the shortest markdown extract worth returning: anything
// smaller is treated as a failed extraction rather than a usable page.
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
	if len(out) < minContentLen {
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

// ── SSRF policy delegation ──
//
// The policy itself lives in internal/netsafe so the MCP HTTP transport can
// share it. Everything below is a thin package-local delegate, kept so the
// existing call sites and tests in this package compile unchanged.

// checkSSRF validates a URL with the shared SSRF policy. It always enforces:
// the package-wide skipSSRFCheck hook only affects the helpers that consult it
// explicitly (newSSRFProtectedClient, checkBrowserTarget, browserRequestAllowed,
// browserHostRule).
func checkSSRF(rawURL string) error { return netsafe.CheckURL(rawURL) }

// resolveValidatedHost resolves host exactly once with the shared policy.
func resolveValidatedHost(ctx context.Context, host string, enforce bool) ([]net.IP, error) {
	return netsafe.ResolveValidatedHost(ctx, host, enforce)
}

// isPrivateIP reports whether ip is not a globally routable unicast address.
func isPrivateIP(ip net.IP) bool { return netsafe.IsBlockedIP(ip) }

// newSSRFProtectedClient returns an *http.Client hardened against SSRF. The
// package-wide test hook disables enforcement so tests can use httptest's
// loopback servers; all production callers run with skipSSRFCheck = false.
func newSSRFProtectedClient(timeout time.Duration) *http.Client {
	return netsafe.NewClient(timeout, !skipSSRFCheck)
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
