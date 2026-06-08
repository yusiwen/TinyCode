package tui

import (
	"strings"
	"testing"
)

func TestMarkdownRoundTrip(t *testing.T) {
	md := "**Key Points:**\n\n1. File structure: A single file `main.go` contains:\n2. HTTP Server setup using `net/http`\n3. Inline code like `http.HandleFunc(\"/\", func(w http.ResponseWriter, r *http.Request) {})`\n4. Response struct containing JSON fields (`message`)\n5. Request processing using an atomic channel"

	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	checks := []string{
		"Key Points",
		"File structure",
		"main.go",
		"net/http",
		"http.HandleFunc",
		"ResponseWriter",
		"JSON fields",
		"message",
		"atomic channel",
	}
	for _, s := range checks {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q\nOutput:\n%s", s, out)
		}
	}
}

func TestMarkdownRoundTripHeading(t *testing.T) {
	md := "## Section Title\n\nParagraph here.\n\n### Subsection\n\nMore text."
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	for _, s := range []string{"Section Title", "Paragraph here", "Subsection", "More text"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripCodeBlock(t *testing.T) {
	md := "Text before:\n\n```go\npackage main\n\nfunc main() {}\n```\n\nText after."
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	for _, s := range []string{"Text before", "Text after", "package main", "func main"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripNestedList(t *testing.T) {
	md := "- Level 1\n  - Level 2\n    - Level 3"
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	for _, s := range []string{"Level 1", "Level 2", "Level 3"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}
