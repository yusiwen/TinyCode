package tui

import (
	"testing"
)

func TestExtractTextCJK(t *testing.T) {
	g := NewCellGrid(20, 3)
	g.Append([]rune("执行make all"), DefaultStyle)
	g.col = 0
	g.row++

	text := g.ExtractText(0, 0, 0, 19)
	if text != "执行make all" {
		t.Errorf("want '执行make all', got %q", text)
	}
}

func TestExtractTextPartialRange(t *testing.T) {
	g := NewCellGrid(20, 3)
	g.Append([]rune("Hello World"), DefaultStyle)
	g.col = 0
	g.row++

	text := g.ExtractText(0, 0, 0, 4)
	if text != "Hello" {
		t.Errorf("want 'Hello', got %q", text)
	}

	text2 := g.ExtractText(0, 6, 0, 10)
	if text2 != "World" {
		t.Errorf("want 'World', got %q", text2)
	}
}

func TestExtractTextMultiLine(t *testing.T) {
	g := NewCellGrid(20, 4)
	g.Append([]rune("Line one"), DefaultStyle)
	g.col = 0
	g.row++
	g.Append([]rune("Line two"), DefaultStyle)
	g.col = 0
	g.row++

	text := g.ExtractText(0, 0, 1, 7)
	if text != "Line one\nLine two" {
		t.Errorf("want 'Line one\\nLine two', got %q", text)
	}
}

func TestExtractTextCJKPartial(t *testing.T) {
	g := NewCellGrid(20, 3)
	g.Append([]rune("执行make all"), DefaultStyle)
	g.col = 0
	g.row++

	// Select "make" (starts at index 2, which is column 4)
	text := g.ExtractText(0, 4, 0, 7)
	if text != "make" {
		t.Errorf("want 'make', got %q", text)
	}
}

func TestRowTextCJK(t *testing.T) {
	g := NewCellGrid(20, 3)
	g.Append([]rune("执行make all"), DefaultStyle)
	g.col = 0
	g.row++

	text := g.RowText(0)
	if text != "执行make all" {
		t.Errorf("want '执行make all', got %q", text)
	}
}
