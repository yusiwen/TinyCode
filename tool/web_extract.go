package tool

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// WebExtract returns a Tool that fetches and extracts content from URLs.
func WebExtract() Tool {
	return Tool{
		Name:        "web_extract",
		Description: "Fetch and extract content from web page URLs. Returns page content in markdown format. " +
			"Max 5 URLs per call. Timeout: 30s per URL. Max response: 2MB.",
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
				} else if content == "" {
					results = append(results, fmt.Sprintf("=== %s ===\nNo content extracted.", urlStr))
				} else {
					if len(content) > 5000 {
						content = content[:5000] + "\n\n[... content truncated at 5000 chars ...]"
					}
					results = append(results, fmt.Sprintf("=== %s ===\n%s", urlStr, content))
				}
			}

			return strings.Join(results, "\n\n"), nil
		},
	}
}

func extractURL(ctx context.Context, urlStr string) (string, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	req, err := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; TinyCode/1.0)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	// Limit body size
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(contentType, "text/html") &&
		!strings.HasPrefix(contentType, "application/xhtml") &&
		contentType == "" {
		// Non-HTML content — return as plain text
		return string(body), nil
	}

	// Parse HTML and extract markdown
	return htmlToMarkdown(string(body))
}

func htmlToMarkdown(htmlContent string) (string, error) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return "", fmt.Errorf("parse html: %w", err)
	}

	// Find <article> or <main> or fallback to <body>
	contentNode := findContentNode(doc)
	if contentNode == nil {
		contentNode = doc
	}

	var sb strings.Builder
	renderNode(contentNode, &sb, 0)
	return strings.TrimSpace(sb.String()), nil
}

func findContentNode(n *html.Node) *html.Node {
	// Priority: <article> > <main> > <body>
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
					break
				}
			}
			var text string
			var textSB strings.Builder
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, &textSB, depth+1)
			}
			text = strings.TrimSpace(textSB.String())
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
			alt := ""
			src := ""
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
			// Skip non-content elements
			return

		default:
			// Skip element wrapper, render children
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				renderNode(c, sb, depth+1)
			}
		}
	}

	// After closing certain block elements, add spacing
	if n.Type == html.ElementNode {
		switch n.Data {
		case "td", "th":
			sb.WriteString("  ")
		case "li":
			sb.WriteString("\n")
		}
	}
}

// sanitize and collapse whitespace
