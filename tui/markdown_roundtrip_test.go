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

func TestMarkdownRoundTripCodeIndent(t *testing.T) {
	// Verify code block indentation is preserved through the pipeline
	md := "```go\npackage main\n\nimport (\n    \"fmt\"\n    \"log\"\n)\n\nfunc main() {\n    log.Println(\"hi\")\n}\n```"
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

	for _, s := range []string{`    "fmt"`, `    "log"`, `    log.Println`} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING indented code: %q\nOutput:\n%s", s, out)
		}
	}
}

func TestMarkdownRoundTripNestedList(t *testing.T) {
	md := "- Level 1\n  - Level 2\n    - Level 3\n      - Level 4"
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

	for _, s := range []string{"Level 1", "Level 2", "Level 3", "Level 4"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripBlockquote(t *testing.T) {
	md := "Normal text.\n\n> Blockquote content here\n\nMore text."
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

	for _, s := range []string{"Normal text", "Blockquote content here", "More text"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripItalic(t *testing.T) {
	md := "Normal *italic* and **bold** and ***both*** text."
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

	for _, s := range []string{"Normal", "italic", "bold", "both", "text"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripTable(t *testing.T) {
	md := "| H1 | H2 | H3 |\n|---|---|---|\n| A | B | C |\n| D | E | F |"
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

	for _, s := range []string{"H1", "H2", "H3", "A", "B", "C", "D", "E", "F"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripLink(t *testing.T) {
	md := "Click [here](https://example.com) for details."
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

	for _, s := range []string{"Click", "here", "for details"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripHr(t *testing.T) {
	md := "Before\n\n---\n\nAfter"
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

	for _, s := range []string{"Before", "After"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripMixed(t *testing.T) {
	md := "# Document\n\nA paragraph with **bold**, *italic*, `code`.\n\n> A quote here\n\n- Item 1\n- Item 2\n\n```go\npackage main\n```\n\nDone."
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

	for _, s := range []string{"Document", "bold", "italic", "code", "quote here",
		"Item 1", "Item 2", "package main", "Done"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}
