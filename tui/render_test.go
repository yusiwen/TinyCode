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
func h3(s string) ContentBlock {
	return ContentBlock{Type: "heading", Level: 3, Chunks: []TextChunk{{Text: s}}}
}
func ul(items ...string) ContentBlock {
	var blockItems []ContentBlock
	for _, item := range items {
		blockItems = append(blockItems, ContentBlock{Chunks: []TextChunk{{Text: item}}})
	}
	return ContentBlock{Type: "list", Items: blockItems}
}
func codeBlock(lang, code string) ContentBlock {
	return ContentBlock{Type: "code", Language: lang, Code: code}
}
func heading(level int, chunks ...TextChunk) ContentBlock {
	return ContentBlock{Type: "heading", Level: level, Chunks: chunks}
}
func tableBlock(headers [][]TextChunk, rows [][][]TextChunk) ContentBlock {
	return ContentBlock{Type: "table", Headers: headers, Rows: rows}
}

// --- Helper ---

func assertContains(t *testing.T, lines []string, sub string) {
	t.Helper()
	for _, line := range lines {
		if strings.Contains(line, sub) {
			return
		}
	}
	t.Errorf("expected %q in rendered output", sub)
}

// --- TestRenderHeading ---

func TestRenderHeading(t *testing.T) {
	lines := renderBlocks([]ContentBlock{heading(1, txt("Title"))}, false)
	assertContains(t, lines, "Title")
}

func TestRenderHeadingLevels(t *testing.T) {
	for level := 1; level <= 3; level++ {
		lines := renderBlocks([]ContentBlock{heading(level, txt("test"))}, false)
		assertContains(t, lines, "test")
	}
}

// --- TestRenderCodeBlock ---

func TestRenderCodeBlock(t *testing.T) {
	lines := renderBlocks([]ContentBlock{codeBlock("go", `package main\n\nfunc main() {}`)}, false)
	assertContains(t, lines, "package main")
	assertContains(t, lines, "func main()")
}

func TestRenderCodeBlockNoLang(t *testing.T) {
	lines := renderBlocks([]ContentBlock{codeBlock("", "echo hi")}, false)
	assertContains(t, lines, "echo hi")
}

// --- TestRenderParagraph ---

func TestRenderParagraphPlain(t *testing.T) {
	lines := renderBlocks([]ContentBlock{para(txt("Hello World"))}, false)
	assertContains(t, lines, "Hello World")
}

func TestRenderParagraphWithStyles(t *testing.T) {
	lines := renderBlocks([]ContentBlock{para(txt("plain"), bold("bold"), italic("italic"), code("code"))}, false)
	assertContains(t, lines, "plain")
	assertContains(t, lines, "bold")
	assertContains(t, lines, "italic")
	assertContains(t, lines, "code")
}

// --- TestRenderList ---

func TestRenderUnorderedList(t *testing.T) {
	blocks := []ContentBlock{
		{Type: "list", Items: []ContentBlock{
			{Chunks: []TextChunk{{Text: "item one"}}},
			{Chunks: []TextChunk{{Text: "item two"}}},
		}},
	}
	lines := renderBlocks(blocks, false)
	assertContains(t, lines, "item one")
	assertContains(t, lines, "item two")
}

func TestRenderOrderedList(t *testing.T) {
	blocks := []ContentBlock{
		{Type: "list", Numbered: true, Items: []ContentBlock{
			{Chunks: []TextChunk{{Text: "first"}}},
			{Chunks: []TextChunk{{Text: "second"}}},
		}},
	}
	lines := renderBlocks(blocks, false)
	assertContains(t, lines, "1.")
	assertContains(t, lines, "2.")
}

// --- TestRenderQuote ---

func TestRenderBlockquote(t *testing.T) {
	blocks := []ContentBlock{
		{Type: "quote", Items: []ContentBlock{
			{Chunks: []TextChunk{{Text: "quoted text"}}},
		}},
	}
	lines := renderBlocks(blocks, false)
	assertContains(t, lines, ">")
	assertContains(t, lines, "quoted")
}

// --- TestRenderHR ---

func TestRenderHR(t *testing.T) {
	block := ContentBlock{Type: "hr"}
	lines := renderBlocks([]ContentBlock{block}, false)
	if len(lines) == 0 || lines[0] == "" {
		t.Error("expected non-empty HR line")
	}
}

// --- TestRenderMultipleBlocks ---

func TestRenderMultipleBlocks(t *testing.T) {
	blocks := []ContentBlock{
		para(txt("First")),
		heading(3, txt("Section")),
		para(txt("Second")),
	}
	lines := renderBlocks(blocks, false)
	assertContains(t, lines, "First")
	assertContains(t, lines, "Section")
	assertContains(t, lines, "Second")
}

// --- TestRenderTable ---

func TestRenderTable(t *testing.T) {
	block := tableBlock(
		[][]TextChunk{{TextChunk{Text: "H1"}}, {TextChunk{Text: "H2"}}},
		[][][]TextChunk{{{TextChunk{Text: "A"}, TextChunk{Text: "B"}}}},
	)
	lines := renderBlocks([]ContentBlock{block}, false)
	assertContains(t, lines, "H1")
	assertContains(t, lines, "H2")
}

func TestRenderTableNoHeader(t *testing.T) {
	block := ContentBlock{
		Type: "table",
		Rows: [][][]TextChunk{{{TextChunk{Text: "data"}}}},
	}
	lines := renderBlocks([]ContentBlock{block}, false)
	assertContains(t, lines, "data")
}
