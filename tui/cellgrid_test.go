package tui

import (
	"strings"
	"testing"
)

func TestCellGridAppend(t *testing.T) {
	g := NewCellGrid(10, 5)
	g.Append([]rune("Hello"), DefaultStyle)
	g.Append([]rune("!"), DefaultStyle)
	g.col = 0
	g.row++

	// "Hello!" should be at row 0, cols 0-5
	cell := g.Get(0, 0)
	if cell.Rune != 'H' {
		t.Errorf("expected H at (0,0), got %c", cell.Rune)
	}
	cell = g.Get(0, 5)
	if cell.Rune != '!' {
		t.Errorf("expected ! at (0,5), got %c", cell.Rune)
	}
}

func TestCellGridWrapping(t *testing.T) {
	g := NewCellGrid(5, 10)
	g.Append([]rune("Hello World"), DefaultStyle)
	g.col = 0
	g.row++

	// "Hello" should fit in row 0, "World" wraps to row 1
	cell := g.Get(0, 0)
	if cell.Rune != 'H' {
		t.Errorf("expected H at (0,0), got %c", cell.Rune)
	}
	cell = g.Get(1, 0)
	if cell.Rune != ' ' {
		t.Errorf("expected space at (1,0), got %c", cell.Rune)
	}
	_ = cell
	_ = g.Get(1, 1)
	// Don't check exact wrapping (width calc), just verify no panic
}

func TestCellGridFill(t *testing.T) {
	g := NewCellGrid(10, 5)
	g.Append([]rune("Hello World!"), DefaultStyle)
	g.col = 0
	g.row++

	// Fill selection on row 0, cols 2-7
	g.Fill(0, 2, 0, 7, SelectionStyle)
	for c := 2; c <= 7; c++ {
		cell := g.Get(0, c)
		if cell.Style.Fg != SelectionStyle.Fg {
			t.Errorf("col %d: expected selection Fg, got %v", c, cell.Style.Fg)
		}
	}
	// Col 1 should not have selection style
	cell := g.Get(0, 1)
	if cell.Style.Fg == SelectionStyle.Fg {
		t.Errorf("col 1 should not have selection Fg")
	}
	_ = cell
}

func TestCellGridExtractText(t *testing.T) {
	g := NewCellGrid(20, 5)
	g.Append([]rune("Hello"), DefaultStyle)
	g.col = 0
	g.row++
	g.Append([]rune("World"), DefaultStyle)
	g.col = 0
	g.row++

	text := g.ExtractText(0, 0, 1, 4)
	if !strings.Contains(text, "Hello") || !strings.Contains(text, "World") {
		t.Errorf("expected both Hello and World, got %q", text)
	}
}

func TestCellGridRender(t *testing.T) {
	g := NewCellGrid(10, 2)
	g.Append([]rune("Hi"), DefaultStyle)
	g.col = 0
	g.row++

	output := g.Render()
	if !strings.Contains(output, "Hi") {
		t.Errorf("expected 'Hi' in render output, got %q", output)
	}
	// Should have 2 rows with newline between
	if !strings.Contains(output, "\n") {
		t.Errorf("expected newline between rows")
	}
}

func TestWordWrapNoWrap(t *testing.T) {
	chunks := wordWrap("Hello", 80, DefaultStyle)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}
	if chunks[0].Text != "Hello" {
		t.Errorf("expected 'Hello', got %q", chunks[0].Text)
	}
}

func TestWordWrapAtBoundary(t *testing.T) {
	chunks := wordWrap("Hello World", 6, DefaultStyle)
	if len(chunks) < 2 {
		t.Fatal("expected at least 2 chunks (wrapped)")
	}
	if !strings.Contains(chunks[0].Text, "Hello") {
		t.Errorf("expected 'Hello' in first chunk, got %q", chunks[0].Text)
	}
	if !strings.Contains(chunks[1].Text, "World") {
		t.Errorf("expected 'World' in second chunk, got %q", chunks[1].Text)
	}
}

func TestWordWrapExactBoundary(t *testing.T) {
	chunks := wordWrap("Hello", 5, DefaultStyle)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk (fits exactly), got %d", len(chunks))
	}
}

func TestWordWrapNewlines(t *testing.T) {
	chunks := wordWrap("Hello\nWorld", 80, DefaultStyle)
	if len(chunks) < 2 {
		t.Fatal("expected at least 2 chunks (newline split)")
	}
	if chunks[0].Text != "Hello" {
		t.Errorf("expected 'Hello', got %q", chunks[0].Text)
	}
	if chunks[1].Text != "World" {
		t.Errorf("expected 'World', got %q", chunks[1].Text)
	}
}

func TestCellGridAppendChunk(t *testing.T) {
	g := NewCellGrid(10, 5)
	g.AppendChunk(CellChunk{Text: "Hello", Style: ThinkingStyle})
	g.AppendChunk(CellChunk{Text: "World", Style: DefaultStyle})

	cell := g.Get(0, 0)
	if cell.Rune != 'H' || cell.Style.Fg != ThinkingStyle.Fg {
		t.Errorf("expected H with thinking style at (0,0)")
	}
	cell = g.Get(1, 0)
	if cell.Rune != 'W' {
		t.Errorf("expected W at row 1, got %c", cell.Rune)
	}
}

func TestCellGridFillMultiLine(t *testing.T) {
	g := NewCellGrid(10, 5)
	g.Append([]rune("Line1"), DefaultStyle)
	g.col = 0
	g.row++
	g.Append([]rune("Line2"), DefaultStyle)
	g.col = 0
	g.row++
	g.Append([]rune("Line3"), DefaultStyle)
	g.col = 0
	g.row++

	// Fill rows 0-1, full columns
	g.Fill(0, 0, 1, 9, SelectionStyle)
	for r := 0; r <= 1; r++ {
		for c := 0; c < 10; c++ {
			cell := g.Get(r, c)
			if cell.Rune != 0 && cell.Style.Fg != SelectionStyle.Fg {
				t.Errorf("(%d,%d) should have selection Fg, got %v", r, c, cell.Style.Fg)
			}
		}
	}
	// Row 2 should NOT have selection
	cell := g.Get(2, 0)
	if cell.Style.Fg == SelectionStyle.Fg {
		t.Errorf("row 2 should not have selection Fg")
	}
	_ = cell
}

func TestCellGridStyleToLipgloss(t *testing.T) {
	// Just verify no panic on various style combinations
	styles := []CellStyle{
		{},
		{Bold: true},
		{Fg: "#FF0000"},
		{Bg: "#0000FF"},
		{Bold: true, Italic: true, Fg: "#FFB347"},
	}
	for _, s := range styles {
		ls := styleToLipgloss(s)
		_ = ls.Render("test")
	}
}
