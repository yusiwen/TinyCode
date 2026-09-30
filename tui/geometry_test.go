package tui

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Regression guards for the degenerate terminal geometry. A pty that was never
// given a window size reports 0x0 to TIOCGWINSZ (verified with `stty size` on
// such a terminal), and before the clamp that panicked in CellGrid.Append with
// "index out of range [0] with length 0".

// brokenSizes are the geometries a real terminal can report on its worst day.
var brokenSizes = []struct {
	name string
	w, h int
}{
	{"zero", 0, 0},
	{"zero-width", 0, 24},
	{"zero-height", 80, 0},
	{"negative", -1, -1},
	{"one-cell", 1, 1},
	{"two-by-one", 2, 1},
	{"tiny", 4, 3},
}

// TestZeroSizeWindowKeepsGeometryUsable drives the real resize handler with a
// lost terminal size and requires a usable, panic-free frame in return: at
// least one cell wide, a non-negative viewport and a positive input width.
func TestZeroSizeWindowKeepsGeometryUsable(t *testing.T) {
	for _, size := range brokenSizes {
		for _, transcript := range []string{"welcome", "conversation"} {
			t.Run(fmt.Sprintf("%s/%s", size.name, transcript), func(t *testing.T) {
				m := newTestTUI()
				m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
				switch transcript {
				case "welcome":
					// The banner the real model seeds itself with at startup.
					m.messages = append(m.messages, newWelcomeMessage(welcomeInfo{Tools: 24, Skills: 2, Agents: 6}))
				case "conversation":
					m.messages = append(m.messages,
						chatMessage{Role: "user", Content: "hello"},
						chatMessage{Role: "assistant", Content: "hi there", Blocks: parseMarkdown("hi there")},
					)
				}

				first := m.View()  // must not panic
				second := m.View() // nor on the incremental render path

				if first == "" || second == "" {
					t.Fatal("View() returned an empty frame")
				}
				if second != first {
					t.Errorf("second render differs from the first\n%s", frameDiff(first, second))
				}
				if m.width < minTerminalWidth || m.height < minTerminalHeight {
					t.Errorf("model geometry = %dx%d, want at least %dx%d",
						m.width, m.height, minTerminalWidth, minTerminalHeight)
				}
				if m.vp.Width < 1 {
					t.Errorf("viewport width = %d, want >= 1", m.vp.Width)
				}
				if m.vp.Height < 0 {
					t.Errorf("viewport height = %d, want >= 0", m.vp.Height)
				}
				if got := m.input.Width(); got < 1 {
					t.Errorf("input width = %d, want >= 1", got)
				}
			})
		}
	}
}

// TestCellGridRefusesZeroArea pins the second line of defence: a zero-area grid
// can no longer be constructed, so a direct Append always has a cell to write.
func TestCellGridRefusesZeroArea(t *testing.T) {
	g := NewCellGrid(0, 0)
	if g.width != minTerminalWidth {
		t.Errorf("width = %d, want %d", g.width, minTerminalWidth)
	}
	g.Append([]rune("x"), CellStyle{})
	if got := g.RowText(0); got != "x" {
		t.Errorf("RowText(0) = %q, want %q", got, "x")
	}
	if got := gridWidth(-5); got != minTerminalWidth {
		t.Errorf("gridWidth(-5) = %d, want %d", got, minTerminalWidth)
	}
}

// TestZeroWidthWindowKeepsIncrementalRender documents the resize comparison: a
// zero-width viewport must not make every frame take the full-rebuild path.
func TestZeroWidthWindowKeepsIncrementalRender(t *testing.T) {
	m := newTestTUI()
	m.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	m.messages = append(m.messages, chatMessage{Role: "user", Content: "hello"})

	m.View()
	grid := m.grid
	m.View() // nothing changed
	if m.grid != grid {
		t.Error("the grid was rebuilt for an unchanged zero-width frame")
	}
	if m.grid == nil || m.grid.width != gridWidth(m.vp.Width) {
		t.Errorf("grid width = %d, want %d", m.grid.width, gridWidth(m.vp.Width))
	}
}
