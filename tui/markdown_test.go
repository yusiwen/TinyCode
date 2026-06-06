package tui

import (
	"strings"
	"testing"
)

// --- Helper ---

func joinLines(lines []string) string {
	return strings.Join(lines, "\n")
}

func assertRender(t *testing.T, md string, expectedSub ...string) []string {
	t.Helper()
	blocks := parseMarkdown(md)
	lines := renderBlocks(blocks, false)
	rendered := joinLines(lines)
	for _, sub := range expectedSub {
		if !strings.Contains(rendered, sub) {
			t.Errorf("rendered output missing %q\nmd: %q\noutput:\n%s", sub, md, rendered)
		}
	}
	return lines
}

func assertBlockTypes(t *testing.T, md string, expected ...string) {
	t.Helper()
	blocks := parseMarkdown(md)
	if len(blocks) != len(expected) {
		t.Errorf("expected %d blocks, got %d\nmd: %q", len(expected), len(blocks), md)
		return
	}
	for i, exp := range expected {
		if blocks[i].Type != exp {
			t.Errorf("block[%d]: expected type %q, got %q\nmd: %q", i, exp, blocks[i].Type, md)
		}
	}
}

// --- Basic inlines ---

func TestParseParagraph(t *testing.T) {
	assertRender(t, "Hello world", "Hello world")
}

func TestParseBold(t *testing.T) {
	assertRender(t, "This is **bold** text", "bold")
}

func TestParseItalic(t *testing.T) {
	assertRender(t, "This is *italic* text", "italic")
}

func TestParseBoldItalic(t *testing.T) {
	assertRender(t, "***both***", "both")
}

func TestParseInlineCode(t *testing.T) {
	assertRender(t, "Use `fmt.Println()` to print", "fmt.Println()")
}

func TestParseLink(t *testing.T) {
	assertRender(t, "Visit [GitHub](https://github.com)", "GitHub")
}

// --- Headings ---

func TestParseHeadings(t *testing.T) {
	assertBlockTypes(t,
		"# H1\n## H2\n### H3\n#### H4\n##### H5\n###### H6",
		"heading", "heading", "heading", "heading", "heading", "heading")
}

func TestParseHeadingContent(t *testing.T) {
	assertRender(t, "## **bold heading**", "bold heading")
}

// --- Lists ---

func TestParseUnorderedList(t *testing.T) {
	_ = assertRender(t, "- item one\n- item two\n- item three",
		"• item one", "• item two", "• item three")
	assertBlockTypes(t, "- first\n- second", "list")
}

func TestParseOrderedList(t *testing.T) {
	_ = assertRender(t, "1. first\n2. second\n3. third",
		"1. first", "2. second", "3. third")
	assertBlockTypes(t, "1. first\n2. second", "list")
}

func TestParseListWithBold(t *testing.T) {
	assertRender(t, "- **bold item**", "• bold item")
}

// --- Code blocks ---

func TestParseCodeBlock(t *testing.T) {
	_ = assertRender(t, "```go\nfunc main() {}\n```", "func main() {}")
	assertBlockTypes(t, "```\ncode\n```", "code")
}

func TestParseCodeBlockNoLanguage(t *testing.T) {
	assertRender(t, "```\nplain code\n```", "plain code")
}

// --- Horizontal rule ---

func TestParseHR(t *testing.T) {
	assertBlockTypes(t, "a\n\n---\n\nb", "paragraph", "hr", "paragraph")
}

// --- Blockquote ---

func TestParseBlockquote(t *testing.T) {
	_ = assertRender(t, "> quoted text", "> quoted text")
	assertBlockTypes(t, "> quote", "quote")
}

// --- Table ---

func TestParseTable(t *testing.T) {
	md := "| H1 | H2 |\n|----|----|\n| A | B |"
	lines := assertRender(t, md, "H1", "H2", "A", "B")
	assertBlockTypes(t, md, "table")
	// Check that box-drawing chars appear
	rendered := joinLines(lines)
	if !strings.Contains(rendered, "│") {
		t.Errorf("expected box-drawing chars in table output:\n%s", rendered)
	}
}

// --- Mixed content ---

func TestParseMixedContent(t *testing.T) {
	md := `# Title

This is a **paragraph** with ` + "`code`" + `.

- list item 1
- list item 2

---

> blockquote

| Col1 | Col2 |
|------|------|
| V1   | V2   |
`
	assertBlockTypes(t, md,
		"heading", "paragraph", "list", "hr", "quote", "table")
}

// --- Edges ---

func TestParseEmptyString(t *testing.T) {
	blocks := parseMarkdown("")
	if blocks != nil {
		t.Errorf("expected nil for empty string, got %d blocks", len(blocks))
	}
}

func TestParseWhitespaceOnly(t *testing.T) {
	blocks := parseMarkdown("   \n  \n  ")
	if blocks != nil {
		t.Errorf("expected nil for whitespace-only, got %d blocks", len(blocks))
	}
}

func TestParseUnclosedBold(t *testing.T) {
	assertRender(t, "**unclosed", "**unclosed")
}

func TestParseCodeSpanInList(t *testing.T) {
	assertRender(t, "- install `go get` package", "go get")
}

func TestParseBoldInHeading(t *testing.T) {
	assertRender(t, "## Summary: **important** info", "important")
}

func TestParseMultipleParagraphs(t *testing.T) {
	md := "First paragraph.\n\nSecond paragraph.\n\nThird paragraph."
	assertBlockTypes(t, md, "paragraph", "paragraph", "paragraph")
}

func TestParseNestedEmphasis(t *testing.T) {
	assertRender(t, "***bold and italic***", "bold and italic")
}

// --- Regression: no duplicate text ---

func TestParseNoDuplicateText(t *testing.T) {
	md := "### 1. **`main.go`** — Entry Point"
	blocks := parseMarkdown(md)
	if len(blocks) == 0 || blocks[0].Type != "heading" {
		t.Fatalf("expected heading block, got %v", blocks)
	}
	var texts []string
	for _, c := range blocks[0].Chunks {
		texts = append(texts, c.Text)
	}
	full := strings.Join(texts, "")
	if strings.Contains(full, "main.gomain.go") {
		t.Errorf("duplicate text detected: %q", full)
	}
	if !strings.Contains(full, "main.go") {
		t.Errorf("expected main.go in %q", full)
	}
}
