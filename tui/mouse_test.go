package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/viewport"
)

// --- helpers ---

func testModelWithMessages(messages []chatMessage) *TuiModel {
	m := &TuiModel{
		ready:        true,
		width:        100,
		height:       50,
		messages:     messages,
		selectStart: -1,
		selectEnd:   -1,
		status:      StatusIdle,
	}
	m.vp = viewport.New(100, 40)
	m.vp.YPosition = 0
	return m
}

func oneMsgModel() *TuiModel {
	return testModelWithMessages([]chatMessage{
		{Role: "user", Content: "Hello"},
	})
}

func twoMsgModel() *TuiModel {
	return testModelWithMessages([]chatMessage{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "World"},
	})
}

// --- Tests ---

func TestMouseWheelScrollUp(t *testing.T) {
	m := oneMsgModel()
	// Content taller than viewport so scrolling works
	m.vp.SetContent(strings.Repeat("line\n", 50))
	m.vp.LineDown(10) // scroll down first
	initial := m.vp.YOffset
	if initial == 0 {
		t.Skip("viewport not scrollable — content may not exceed height")
	}

	m.Update(tea.MouseMsg{
		Button: tea.MouseButtonWheelUp,
	})

	if m.vp.YOffset >= initial {
		t.Errorf("expected YOffset to decrease after wheel up, was %d, now %d", initial, m.vp.YOffset)
	}
}

func TestMouseWheelScrollDown(t *testing.T) {
	m := oneMsgModel()
	// Content taller than viewport
	m.vp.SetContent(strings.Repeat("line\n", 50))
	initial := m.vp.YOffset

	m.Update(tea.MouseMsg{
		Button: tea.MouseButtonWheelDown,
	})

	if m.vp.YOffset <= initial {
		t.Errorf("expected YOffset to increase after wheel down, was %d, now %d", initial, m.vp.YOffset)
	}
}

func TestMousePressSelectsFirstMessage(t *testing.T) {
	m := twoMsgModel()
	// Y=1 maps to contentLine=0 (first message)
	m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		X:      5, Y: 1,
	})
	if m.selectStart != 0 {
		t.Errorf("expected selectStart=0, got %d", m.selectStart)
	}
	if m.selectEnd != 0 {
		t.Errorf("expected selectEnd=0, got %d", m.selectEnd)
	}
	if !m.selecting {
		t.Error("expected selecting=true on press")
	}
}

func TestMousePressOnSecondMessage(t *testing.T) {
	m := twoMsgModel()
	// Y=3 maps to contentLine=2 (past first msg, reaching second msg)
	m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		X:      5, Y: 3,
	})
	// After the user message (1 line), we reach the assistant message
	if m.selectStart != 1 {
		t.Errorf("expected selectStart=1, got %d", m.selectStart)
	}
}

func TestMouseDragExtendsSelection(t *testing.T) {
	m := testModelWithMessages([]chatMessage{
		{Role: "user", Content: "U1"},
		{Role: "assistant", Content: "A1"},
		{Role: "system", Content: "S1"},
		{Role: "user", Content: "U2"},
	})
	m.selecting = true
	m.mouseDrag = true
	m.selectStart = 0
	m.selectEnd = 0

	// Drag to Y=5 — contentLine=4 reaches the 4th message (3 lines past msg0)
	m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionMotion,
		X:      5, Y: 5,
	})
	if m.selectEnd < 2 {
		t.Errorf("expected selectEnd >= 2 after drag, got %d", m.selectEnd)
	}
}

func TestMouseReleaseWithoutDrag(t *testing.T) {
	m := twoMsgModel()
	m.mouseDrag = false
	m.selectStart = 0
	m.selectEnd = 0

	m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
		X:      5, Y: 5,
	})
	// No drag → selection should be cleared
	if m.selectStart != -1 {
		t.Errorf("expected selectStart=-1 (no drag), got %d", m.selectStart)
	}
}

func TestMouseReleaseWithDrag(t *testing.T) {
	m := twoMsgModel()
	m.mouseDrag = true
	m.selectStart = 0
	m.selectEnd = 1

	m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionRelease,
		X:      5, Y: 5,
	})
	// After release, selecting should be false but selection preserved
	if m.selecting {
		t.Error("expected selecting=false after release")
	}
	if m.selectStart != 0 {
		t.Errorf("expected selectStart=0, got %d", m.selectStart)
	}
	if m.selectEnd != 1 {
		t.Errorf("expected selectEnd=1, got %d", m.selectEnd)
	}
}

func TestReadyGuard(t *testing.T) {
	m := &TuiModel{ready: false}
	_, cmd := m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		X:      5, Y: 2,
	})
	if cmd != nil {
		t.Error("expected nil cmd when not ready")
	}
	if m.ready {
		t.Error("expected ready to remain false")
	}
}

func TestSelectionClearsOnNewPress(t *testing.T) {
	m := twoMsgModel()
	m.selectStart = 0
	m.selectEnd = 1

	m.Update(tea.MouseMsg{
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
		X:      5, Y: 3,
	})
	// New press should start fresh selection
	if m.selectStart != 1 {
		t.Errorf("expected selectStart=1 (second msg), got %d", m.selectStart)
	}
}

func TestIsSelected(t *testing.T) {
	tests := []struct {
		name     string
		start    int
		end      int
		index    int
		expected bool
	}{
		{"no selection", -1, -1, 0, false},
		{"single msg selected", 1, 1, 1, true},
		{"single msg not selected", 1, 1, 0, false},
		{"range start", 0, 2, 0, true},
		{"range middle", 0, 2, 1, true},
		{"range end", 0, 2, 2, true},
		{"range outside", 0, 2, 3, false},
		{"reverse start>end", 2, 0, 0, true}, // selectStart=2, selectEnd=0 → selects 0-2
		{"reverse middle", 2, 0, 1, true},
		{"reverse end", 2, 0, 2, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := testModelWithMessages([]chatMessage{
				{Role: "user", Content: "a"},
				{Role: "assistant", Content: "b"},
				{Role: "system", Content: "c"},
			})
			m.selectStart = tt.start
			m.selectEnd = tt.end
			got := m.isSelected(tt.index)
			if got != tt.expected {
				t.Errorf("isSelected(%d) = %v, want %v (start=%d, end=%d)",
					tt.index, got, tt.expected, tt.start, tt.end)
			}
		})
	}
}

func TestSelectedMessages(t *testing.T) {
	m := testModelWithMessages([]chatMessage{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "World", ReasoningContent: "thinking..."},
		{Role: "system", Content: "Done"},
	})
	m.selectStart = 0
	m.selectEnd = 2
	text := m.selectedMessages()
	if !strings.Contains(text, "Hello") {
		t.Errorf("expected 'Hello' in selected text, got: %q", text)
	}
	if !strings.Contains(text, "thinking...") {
		t.Errorf("expected reasoning in selected text, got: %q", text)
	}
	if !strings.Contains(text, "World") {
		t.Errorf("expected 'World' in selected text, got: %q", text)
	}
	if !strings.Contains(text, "Done") {
		t.Errorf("expected 'Done' in selected text, got: %q", text)
	}
}

func TestSelectedMessagesNoSelection(t *testing.T) {
	m := oneMsgModel()
	m.selectStart = -1
	m.selectEnd = -1
	text := m.selectedMessages()
	if text != "" {
		t.Errorf("expected empty for no selection, got: %q", text)
	}
}

func TestMessageAtLine(t *testing.T) {
	m := testModelWithMessages([]chatMessage{
		{Role: "user", Content: "U1"},
		{Role: "assistant", Content: "A1", ReasoningContent: "R1"},
		{Role: "system", Content: "S1"},
	})
	m.width = 100

	// messageAtLine(0) should be the user message
	idx := m.messageAtLine(0)
	if idx != 0 {
		t.Errorf("messageAtLine(0): expected 0, got %d", idx)
	}

	// messageAtLine(1) should be the assistant message (1 line for reasoning)
	idx = m.messageAtLine(1)
	if idx != 1 {
		t.Errorf("messageAtLine(1): expected 1, got %d", idx)
	}

	// messageAtLine(999) should be -1 (past end)
	idx = m.messageAtLine(999)
	if idx != -1 {
		t.Errorf("messageAtLine(999): expected -1, got %d", idx)
	}
}

func TestVisibleLines(t *testing.T) {
	tests := []struct {
		name  string
		input string
		width int
		want  int
	}{
		{"empty", "", 80, 0},
		{"short", "hello", 80, 1},
		{"wrapped", strings.Repeat("x", 200), 80, 3}, // 200/80 = 3 lines
		{"exact", strings.Repeat("x", 80), 80, 1},
		{"one over", strings.Repeat("x", 81), 80, 2},
		{"multiline", "line1\nline2\nline3", 80, 3},
		{"zero width", "hello", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := visibleLines(tt.input, tt.width)
			if got != tt.want {
				t.Errorf("visibleLines(%q, %d) = %d, want %d", tt.input[:min(len(tt.input), 20)], tt.width, got, tt.want)
			}
		})
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
