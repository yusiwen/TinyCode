package tool

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// searxngURL can be set to a self-hosted SearXNG instance.
// Empty = not configured (DuckDuckGo only).
var searxngURL = ""

type searchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Position    int    `json:"position"`
}

// WebSearch returns a Tool that searches the web.
// Primary: DuckDuckGo Lite (zero config). Optional: SearXNG if configured.
func WebSearch() Tool {
	return Tool{
		Name:        "web_search",
		Description: "Search the web for information. " +
			"Uses DuckDuckGo Lite (no API key needed) with optional SearXNG fallback. " +
			"Returns up to 5 results by default with titles, URLs, and descriptions.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{
					"type":        "string",
					"description": "The search query",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of results (1-20, default 5)",
				},
			},
			"required": []string{"query"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			query, _ := args["query"].(string)
			if query == "" {
				return "", fmt.Errorf("query is required")
			}

			limit := 5
			switch v := args["limit"].(type) {
			case float64:
				limit = int(v)
			case int:
				limit = v
			}
			if limit > 20 {
				limit = 20
			}
			if limit < 1 {
				limit = 1
			}

			// Try DuckDuckGo first
			results, err := searchDuckDuckGo(ctx, query, limit)
			if err != nil {
				// Fallback to SearXNG if configured
				if searxngURL != "" {
					results, err = searchSearXNG(ctx, query, limit)
					if err != nil {
						return "", fmt.Errorf("web_search (DDG+searxng): %w", err)
					}
				} else {
					return "", fmt.Errorf("web_search: %w", err)
				}
			}
			if len(results) == 0 {
				return "No search results found.", nil
			}

			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Web search results for %q:\n\n", query))
			for _, r := range results {
				desc := strings.TrimSpace(r.Description)
				if len(desc) > 150 {
					desc = desc[:147] + "..."
				}
				sb.WriteString(fmt.Sprintf("%d. %s\n   %s\n   %s\n\n", r.Position, r.Title, r.URL, desc))
			}
			return strings.TrimSpace(sb.String()), nil
		},
	}
}

// SetSearXNG configures the SearXNG search backend URL.
func SetSearXNG(baseURL string) {
	searxngURL = strings.TrimRight(baseURL, "/")
}

// ── DuckDuckGo Lite backend ──

var ddgLiteURL = "https://lite.duckduckgo.com/lite/"

func searchDuckDuckGo(ctx context.Context, query string, limit int) ([]searchResult, error) {
	client := &http.Client{Timeout: 15 * time.Second}

	req, err := http.NewRequestWithContext(ctx, "POST", ddgLiteURL,
		strings.NewReader(url.Values{"q": {query}}.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; TinyCode/1.0)")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("DuckDuckGo returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return parseDuckDuckGoResults(string(body), limit)
}

func parseDuckDuckGoResults(htmlContent string, limit int) ([]searchResult, error) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	var results []searchResult
	position := 0
	seen := make(map[string]bool)

	var f func(*html.Node)
	f = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			var href, linkText string
			isResult := false
			for _, attr := range n.Attr {
				if attr.Key == "class" && strings.Contains(attr.Val, "result-link") {
					isResult = true
				}
				if attr.Key == "href" {
					href = attr.Val
				}
			}
			if isResult && href != "" {
				if n.FirstChild != nil {
					linkText = extractText(n)
				}
				if !seen[href] {
					seen[href] = true
					position++
					desc := findDescription(n)
					results = append(results, searchResult{
						Title:       strings.TrimSpace(linkText),
						URL:         href,
						Description: desc,
						Position:    position,
					})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			f(c)
		}
	}
	f(doc)

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func extractText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

func findDescription(linkNode *html.Node) string {
	for sib := linkNode.Parent; sib != nil; sib = sib.NextSibling {
		if sib.Type == html.ElementNode && sib.Data == "td" {
			for _, attr := range sib.Attr {
				if attr.Key == "class" && strings.Contains(attr.Val, "result-snippet") {
					return strings.TrimSpace(extractText(sib))
				}
			}
		}
	}
	for sib := linkNode.Parent.NextSibling; sib != nil; sib = sib.NextSibling {
		if sib.Type == html.ElementNode {
			var findSnippet func(*html.Node) string
			findSnippet = func(n *html.Node) string {
				if n.Type == html.ElementNode {
					for _, attr := range n.Attr {
						if attr.Key == "class" && strings.Contains(attr.Val, "result-snippet") {
							return strings.TrimSpace(extractText(n))
						}
					}
				}
				for c := n.FirstChild; c != nil; c = c.NextSibling {
					if s := findSnippet(c); s != "" {
						return s
					}
				}
				return ""
			}
			if s := findSnippet(sib); s != "" {
				return s
			}
		}
	}
	return ""
}

// ── SearXNG backend ──

func searchSearXNG(ctx context.Context, query string, limit int) ([]searchResult, error) {
	if searxngURL == "" {
		return nil, fmt.Errorf("SearXNG not configured")
	}

	u := fmt.Sprintf("%s/search?format=json&q=%s", searxngURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; TinyCode/1.0)")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("SearXNG returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return parseSearXNGResults(string(body), limit)
}

func parseSearXNGResults(jsonContent string, limit int) ([]searchResult, error) {
	// SearXNG returns JSON with a `results` array.
	// We parse using a simple approach: find the results array and extract items.
	var results []searchResult

	// Find "results":[ or "results": [
	idx := strings.Index(jsonContent, `"results"`)
	if idx < 0 {
		return nil, fmt.Errorf("no results in SearXNG response")
	}
	// Find the opening bracket
	arrStart := strings.IndexByte(jsonContent[idx:], '[')
	if arrStart < 0 {
		return nil, fmt.Errorf("no results array")
	}
	arrStart += idx

	// Find the matching closing bracket (simple: count)
	depth := 0
	arrEnd := -1
	for i := arrStart; i < len(jsonContent); i++ {
		switch jsonContent[i] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				arrEnd = i
				i = len(jsonContent) // break outer
			}
		}
	}
	if arrEnd < 0 {
		return nil, fmt.Errorf("unclosed results array")
	}

	// Parse individual items
	arrContent := jsonContent[arrStart : arrEnd+1]
	// Split by "{" to get individual result objects
	itemStart := strings.IndexByte(arrContent, '{')
	position := 0
	for itemStart >= 0 && position < limit {
		depth = 0
		itemEnd := -1
		for i := itemStart; i < len(arrContent); i++ {
			switch arrContent[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					itemEnd = i + 1
					i = len(arrContent)
				}
			}
		}
		if itemEnd < 0 {
			break
		}
		item := arrContent[itemStart:itemEnd]
		itemStart = strings.IndexByte(arrContent[itemEnd:], '{')
		if itemStart >= 0 {
			itemStart += itemEnd
		}

		var r searchResult
		r.Title = extractJSONField(item, "title")
		r.URL = extractJSONField(item, "url")
		r.Description = extractJSONField(item, "content")
		if r.Title == "" && r.URL == "" {
			continue
		}
		position++
		r.Position = position
		results = append(results, r)
	}

	return results, nil
}

func extractJSONField(jsonStr, field string) string {
	pattern := fmt.Sprintf(`"%s"`, field)
	idx := strings.Index(jsonStr, pattern)
	if idx < 0 {
		return ""
	}
	// Find the value after ":"
	valStart := strings.IndexByte(jsonStr[idx+len(pattern):], ':')
	if valStart < 0 {
		return ""
	}
	valStart += idx + len(pattern) + 1
	// Skip whitespace
	for valStart < len(jsonStr) && jsonStr[valStart] == ' ' {
		valStart++
	}
	if valStart >= len(jsonStr) {
		return ""
	}

	// Check if string value (") or other
	if jsonStr[valStart] == '"' {
		valStart++
		var sb strings.Builder
		for i := valStart; i < len(jsonStr); i++ {
			ch := jsonStr[i]
			if ch == '\\' && i+1 < len(jsonStr) {
				sb.WriteByte(jsonStr[i+1])
				i++
				continue
			}
			if ch == '"' {
				return sb.String()
			}
			sb.WriteByte(ch)
		}
		return sb.String()
	}

	// Non-string value (number, boolean, null)
	end := valStart
	for end < len(jsonStr) {
		ch := jsonStr[end]
		if ch == ',' || ch == '}' || ch == ']' {
			break
		}
		end++
	}
	return strings.TrimSpace(jsonStr[valStart:end])
}
