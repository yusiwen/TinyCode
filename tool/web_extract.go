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
		Name:        "web_extract",
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
							summary, err := summarizer(context.Background(), content)
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
	client := &http.Client{Timeout: 30 * time.Second}
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
	client := &http.Client{Timeout: 15 * time.Second}
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

func checkSSRF(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	host := u.Hostname()

	blockedHosts := map[string]bool{
		"169.254.169.254":          true,
		"169.254.170.2":            true,
		"169.254.169.253":          true,
		"metadata.google.internal": true,
		"metadata.goog":            true,
		"100.100.100.200":          true,
	}
	if blockedHosts[host] {
		return fmt.Errorf("SSRF: blocked host %q (cloud metadata)", host)
	}

	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("SSRF: DNS resolution failed for %q: %w", host, err)
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("SSRF: blocked private IP %q for host %q", ipStr, host)
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() {
		return true
	}
	if ip.IsLinkLocalUnicast() {
		return true
	}
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		case ip4[0] == 127:
			return true
		}
	}
	if ip.To16() != nil {
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() {
			return true
		}
	}
	return false
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
