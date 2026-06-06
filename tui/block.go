package tui

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extensionast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// TextChunk is a run of inline text with uniform styling.
type TextChunk struct {
	Text   string
	Bold   bool
	Italic bool
	Code   bool
	Link   string
}

// ContentBlock is a top-level block in the rendered message.
type ContentBlock struct {
	Type     string         // "paragraph", "heading", "code", "list", "quote", "hr", "table"
	Chunks   []TextChunk    // inline content
	Level    int            // heading level
	Language string         // code block language
	Code     string         // raw code text
	Items    []ContentBlock // list items / quote children
	Numbered bool           // ordered list
	Headers  [][]TextChunk  // table header cells
	Rows     [][][]TextChunk // table body cells
}

// parseMarkdown converts markdown text into a slice of ContentBlocks.
func parseMarkdown(md string) []ContentBlock {
	if strings.TrimSpace(md) == "" {
		return nil
	}

	reader := text.NewReader([]byte(md))
	gm := goldmark.New(
		goldmark.WithExtensions(extension.Table),
	)
	doc := gm.Parser().Parse(reader)
	source := reader.Source()

	var blocks []ContentBlock

	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		// Table nodes are registered in extension/ast
		if n.Kind() == extensionast.KindTable {
			blocks = append(blocks, collectTable(n, source))
			return ast.WalkSkipChildren, nil
		}
		switch node := n.(type) {
		case *ast.Paragraph:
			blocks = append(blocks, ContentBlock{
				Type:   "paragraph",
				Chunks: collectInline(node, source),
			})

		case *ast.Heading:
			blocks = append(blocks, ContentBlock{
				Type:   "heading",
				Level:  node.Level,
				Chunks: collectInline(node, source),
			})

		case *ast.FencedCodeBlock:
			var code strings.Builder
			lines := node.Lines()
			for i := 0; i < lines.Len(); i++ {
				seg := lines.At(i)
				code.Write(seg.Value(source))
			}
			blocks = append(blocks, ContentBlock{
				Type:     "code",
				Language: string(node.Language(source)),
				Code:     strings.TrimRight(code.String(), "\n"),
			})

		case *ast.List:
			listBlock := collectListItems(node, source, node.IsOrdered())
			if listBlock != nil {
				blocks = append(blocks, *listBlock)
			}
			return ast.WalkSkipChildren, nil

		case *ast.Blockquote:
			var quoteItems []ContentBlock
			for child := n.FirstChild(); child != nil; child = child.NextSibling() {
				if p, ok := child.(*ast.Paragraph); ok {
					quoteItems = append(quoteItems, ContentBlock{
						Type:   "paragraph",
						Chunks: collectInline(p, source),
					})
				}
			}
			if len(quoteItems) > 0 {
				blocks = append(blocks, ContentBlock{
					Type:  "quote",
					Items: quoteItems,
				})
			}
			return ast.WalkSkipChildren, nil

		case *ast.ThematicBreak:
			blocks = append(blocks, ContentBlock{Type: "hr"})
		}
		return ast.WalkContinue, nil
	})

	return blocks
}

// collectInline walks inline children of a node and returns styled TextChunks.
func collectInline(n ast.Node, source []byte) []TextChunk {
	var chunks []TextChunk
	var buf strings.Builder
	bold := false
	italic := false
	code := false
	link := ""

	flush := func() {
		if buf.Len() > 0 {
			chunks = append(chunks, TextChunk{
				Text: buf.String(), Bold: bold, Italic: italic, Code: code, Link: link,
			})
			buf.Reset()
		}
	}

	ast.Walk(n, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		switch node := child.(type) {
		case *ast.Text:
			if entering {
				seg := node.Segment
				buf.Write(seg.Value(source))
			}
		case *ast.RawHTML:
			// skip raw HTML in markdown
		case *ast.String:
			if entering {
				buf.WriteString(string(node.Value))
			}
		case *ast.Emphasis:
			if entering {
				flush()
				if node.Level == 2 {
					bold = true
				} else {
					italic = true
				}
			} else {
				flush()
				if node.Level == 2 {
					bold = false
				} else {
					italic = false
				}
			}
		case *ast.CodeSpan:
			if entering {
				flush()
				code = true
				// Read code text from children manually
				for c := child.FirstChild(); c != nil; c = c.NextSibling() {
					if t, ok := c.(*ast.Text); ok {
						buf.Write(t.Segment.Value(source))
					}
					if s, ok := c.(*ast.String); ok {
						buf.WriteString(string(s.Value))
					}
				}
				return ast.WalkSkipChildren, nil
			}
			flush()
			code = false
		case *ast.Link:
			if entering {
				flush()
				link = string(node.Destination)
			} else {
				flush()
				link = ""
			}
		case *ast.AutoLink:
			if entering {
				buf.WriteString(string(node.URL(source)))
				link = string(node.URL(source))
			} else {
				flush()
				link = ""
			}
		}
		return ast.WalkContinue, nil
	})
	flush()
	return chunks
}

// collectListItems processes list nodes and returns a ContentBlock for the list.
func collectListItems(n *ast.List, source []byte, numbered bool) *ContentBlock {
	var items []ContentBlock
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		li, ok := child.(*ast.ListItem)
		if !ok {
			continue
		}
		// A list item may contain a paragraph, or multiple blocks
		for block := li.FirstChild(); block != nil; block = block.NextSibling() {
			switch b := block.(type) {
			case *ast.Paragraph:
				chunks := collectInline(b, source)
				items = append(items, ContentBlock{
					Type:   "paragraph",
					Chunks: chunks,
				})
			case *ast.TextBlock:
				// Simple list items use TextBlock instead of Paragraph
				chunks := collectInline(b, source)
				items = append(items, ContentBlock{
					Type:   "paragraph",
					Chunks: chunks,
				})
			case *ast.FencedCodeBlock:
				var code strings.Builder
				lines := b.Lines()
				for i := 0; i < lines.Len(); i++ {
					seg := lines.At(i)
					code.Write(seg.Value(source))
				}
				items = append(items, ContentBlock{
					Type:     "code",
					Language: string(b.Language(source)),
					Code:     strings.TrimRight(code.String(), "\n"),
				})
			}
		}
	}
	if len(items) == 0 {
		return nil
	}
	return &ContentBlock{
		Type:     "list",
		Items:    items,
		Numbered: numbered,
	}
}

// collectTable processes a table AST node and returns a ContentBlock.
func collectTable(n ast.Node, source []byte) ContentBlock {
	block := ContentBlock{Type: "table"}
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch child.Kind() {
		case extensionast.KindTableHeader:
			block.Headers = collectTableRowCells(child, source)
		case extensionast.KindTableRow:
			cells := collectTableRowCells(child, source)
			block.Rows = append(block.Rows, cells)
		}
	}
	return block
}

// collectTableRowCells extracts the text from each cell in a table row.
func collectTableRowCells(n ast.Node, source []byte) [][]TextChunk {
	var cells [][]TextChunk
	for cell := n.FirstChild(); cell != nil; cell = cell.NextSibling() {
		if cell.Kind() == extensionast.KindTableCell {
			chunks := collectInline(cell, source)
			cells = append(cells, chunks)
		}
	}
	return cells
}
