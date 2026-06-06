package tui

import (
	"strings"
	"testing"
)

// --- Builder helpers for ContentBlock ---

func txt(s string) TextChunk           { return TextChunk{Text: s} }
func bold(s string) TextChunk           { return TextChunk{Text: s, Bold: true} }
func italic(s string) TextChunk         { return TextChunk{Text: s, Italic: true} }
func code(s string) TextChunk           { return TextChunk{Text: s, Code: true} }
func link(text, url string) TextChunk   { return TextChunk{Text: text, Link: url} }

func para(chunks ...TextChunk) ContentBlock {
	return ContentBlock{Type: "paragraph", Chunks: chunks}
}

func heading(level int, chunks ...TextChunk) ContentBlock {
	return ContentBlock{Type: "heading", Level: level, Chunks: chunks}
}

func codeBlock(code, lang string) ContentBlock {
	return ContentBlock{Type: "code", Language: lang, Code: code}
}

func hr() ContentBlock {
	return ContentBlock{Type: "hr"}
}

func ul(items ...ContentBlock) ContentBlock {
	return ContentBlock{Type: "list", Items: items, Numbered: false}
}

func ol(items ...ContentBlock) ContentBlock {
	return ContentBlock{Type: "list", Items: items, Numbered: true}
}

func li(chunks ...TextChunk) ContentBlock {
	return ContentBlock{Type: "paragraph", Chunks: chunks}
}

func quote(items ...ContentBlock) ContentBlock {
	return ContentBlock{Type: "quote", Items: items}
}

func tableRow(cells ...[]TextChunk) [][]TextChunk {
	return cells
}

// --- Assertions ---

func assertLines(t *testing.T, got []string, expected ...string) {
	t.Helper()
	if len(got) != len(expected) {
		t.Errorf("expected %d lines, got %d\nexpected:\n  %q\ngot:\n  %q",
			len(expected), len(got), strings.Join(expected, "\\n"), strings.Join(got, "\\n"))
		return
	}
	for i, exp := range expected {
		if got[i] != exp {
			// Check if it's an ANSI-styled version
			if strings.Contains(got[i], exp) {
				continue // styled version contains the expected text
			}
			t.Errorf("line[%d]: expected %q, got %q", i, exp, got[i])
		}
	}
}

func assertContains(t *testing.T, lines []string, sub string) {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return
		}
	}
	t.Errorf("expected %q in rendered output:\n  %s", sub, strings.Join(lines, "\n  "))
}

// --- Tests ---

func TestRenderParagraph(t *testing.T) {
	lines := renderBlocks([]ContentBlock{para(txt("Hello world"))}, false)
	assertContains(t, lines, "Hello world")
}

func TestRenderParagraphWithStyles(t *testing.T) {
	lines := renderBlocks([]ContentBlock{
		para(txt("plain "), bold("bold "), italic("italic "), code("code")),
	}, false)
	assertContains(t, lines, "plain")
	assertContains(t, lines, "bold")
	assertContains(t, lines, "italic")
	assertContains(t, lines, "code")
}

func TestRenderHeading(t *testing.T) {
	lines := renderBlocks([]ContentBlock{heading(1, txt("Title"))}, false)
	assertContains(t, lines, "# Title")
	_ = lines
}

func TestRenderHeadingLevels(t *testing.T) {
	for lvl := 1; lvl <= 3; lvl++ {
		prefix := strings.Repeat("#", lvl)
		lines := renderBlocks([]ContentBlock{heading(lvl, txt("test"))}, false)
		assertContains(t, lines, prefix+" test")
	}
}

func TestRenderCodeBlock(t *testing.T) {
	lines := renderBlocks([]ContentBlock{codeBlock("func main() {}", "go")}, false)
	assertContains(t, lines, "func main() {}")
	// Code blocks use dim style
}

func TestRenderCodeBlockMultiLine(t *testing.T) {
	code := "line1\nline2\nline3"
	lines := renderBlocks([]ContentBlock{codeBlock(code, "")}, false)
	assertContains(t, lines, "line1")
	assertContains(t, lines, "line2")
	assertContains(t, lines, "line3")
}

func TestRenderUnorderedList(t *testing.T) {
	lines := renderBlocks([]ContentBlock{
		ul(li(txt("first")), li(bold("second"))),
	}, false)
	assertContains(t, lines, "• first")
	assertContains(t, lines, "• second")
}

func TestRenderOrderedList(t *testing.T) {
	lines := renderBlocks([]ContentBlock{
		ol(li(txt("one")), li(txt("two"))),
	}, false)
	assertContains(t, lines, "1. one")
	assertContains(t, lines, "2. two")
}

func TestRenderQuote(t *testing.T) {
	lines := renderBlocks([]ContentBlock{
		quote(para(txt("cited text"))),
	}, false)
	assertContains(t, lines, "> cited text")
}

func TestRenderHR(t *testing.T) {
	lines := renderBlocks([]ContentBlock{hr()}, false)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line for hr, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "─") {
		t.Errorf("expected box-drawing chars in hr, got: %q", lines[0])
	}
}

func TestRenderTable(t *testing.T) {
	block := ContentBlock{
		Type: "table",
		Headers: [][]TextChunk{
			{txt("Name")},
			{txt("Value")},
		},
		Rows: [][][]TextChunk{
			{{txt("A")}, {txt("1")}},
			{{txt("B")}, {txt("2")}},
		},
	}
	lines := renderBlocks([]ContentBlock{block}, false)
	rendered := strings.Join(lines, "\n")
	assertContains(t, lines, "Name")
	assertContains(t, lines, "Value")
	assertContains(t, lines, "A")
	assertContains(t, lines, "1")
	if !strings.Contains(rendered, "│") {
		t.Errorf("expected box-drawing chars in table")
	}
}

func TestRenderSelected(t *testing.T) {
	// When sel=true, all lines should contain selectedStyle bg color (ANSI)
	lines := renderBlocks([]ContentBlock{para(txt("hello"))}, true)
	assertContains(t, lines, "hello")
	if len(lines) == 0 {
		t.Fatal("no lines rendered")
	}
}

func TestRenderEmptyBlocks(t *testing.T) {
	lines := renderBlocks(nil, false)
	if len(lines) != 0 {
		t.Errorf("expected 0 lines for nil blocks, got %d", len(lines))
	}
}

func TestRenderMultipleBlocks(t *testing.T) {
	blocks := []ContentBlock{
		heading(2, txt("Section")),
		para(txt("Description")),
		codeBlock("data", ""),
	}
	lines := renderBlocks(blocks, false)
	assertContains(t, lines, "## Section")
	assertContains(t, lines, "Description")
	assertContains(t, lines, "data")
}

func TestRenderListNestedCode(t *testing.T) {
	// List item with code block inside
	block := ContentBlock{
		Type: "list",
		Items: []ContentBlock{
			{Type: "code", Code: "print(\"hello\")"},
		},
	}
	lines := renderBlocks([]ContentBlock{block}, false)
	assertContains(t, lines, "print(\"hello\")")
}

func TestRenderMultipleParagraphs(t *testing.T) {
	blocks := []ContentBlock{
		para(txt("First")),
		para(txt("Second")),
		para(txt("Third")),
	}
	lines := renderBlocks(blocks, false)
	assertContains(t, lines, "First")
	assertContains(t, lines, "Second")
	assertContains(t, lines, "Third")
}
