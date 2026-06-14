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

var ddgLiteURL = "https://lite.duckduckgo.com/lite/"

type searchResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	Position    int    `json:"position"`
}

// WebSearch returns a Tool that searches the web using DuckDuckGo Lite.
// Zero configuration required — no API key.
func WebSearch() Tool {
	return Tool{
		Name:        "web_search",
		Description: "Search the web for information. Uses DuckDuckGo Lite (no API key needed). " +
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

			results, err := searchDuckDuckGo(ctx, query, limit)
			if err != nil {
				return "", fmt.Errorf("web_search: %w", err)
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

	// Find all <a> tags with class="result-link"
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
				// Extract text from <a> children
				if n.FirstChild != nil {
					linkText = extractText(n)
				}
				// Deduplicate
				if !seen[href] {
					seen[href] = true
					position++

					// Find the description (sibling .result-snippet)
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
	// The description is in the next sibling's child with class "result-snippet"
	for sib := linkNode.Parent; sib != nil; sib = sib.NextSibling {
		if sib.Type == html.ElementNode && sib.Data == "td" {
			for _, attr := range sib.Attr {
				if attr.Key == "class" && strings.Contains(attr.Val, "result-snippet") {
					return strings.TrimSpace(extractText(sib))
				}
			}
		}
	}

	// Fallback: search from parent's next sibling
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
