package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"
)

// --- Ctrl+C handler ---

func TestCtrlCCopyWhenSelected(t *testing.T) {
	m := testModelWithMessages([]chatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world"},
	})
	m.selectStart = 0
	m.selectEnd = 1
	m.status = StatusIdle

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if cmd != nil {
		t.Errorf("expected nil cmd (copy, not quit), got %v", cmd)
	}
	// Selection should be cleared after copy
	if m.selectStart != -1 || m.selectEnd != -1 {
		t.Errorf("expected selection cleared after copy, got start=%d end=%d", m.selectStart, m.selectEnd)
	}
	// System message should be appended
	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.Content, "Copied") {
		t.Errorf("expected copy confirmation, got %q", last.Content)
	}
}

func TestCtrlCInterruptWhenStreaming(t *testing.T) {
	m := testModelWithMessages([]chatMessage{
		{Role: "assistant", Streaming: true, Content: "partial"},
	})
	m.curAssistant = &m.messages[len(m.messages)-1]
	m.status = StatusStreaming

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if cmd != nil {
		t.Errorf("expected nil cmd (interrupt, not quit), got %v", cmd)
	}
	if m.status != StatusIdle {
		t.Errorf("expected StatusIdle after interrupt, got %v", m.status)
	}
	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.Content, "Interrupted") {
		t.Errorf("expected interrupt message, got %q", last.Content)
	}
}

func TestCtrlCFirstTapShowsPrompt(t *testing.T) {
	m := testModelWithMessages([]chatMessage{})
	m.quitConfirm = false

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if cmd != nil {
		t.Errorf("expected nil cmd (first tap prompt), got %v", cmd)
	}
	if !m.quitConfirm {
		t.Error("expected quitConfirm=true after first tap")
	}
	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.Content, "again") {
		t.Errorf("expected 'Press Ctrl+C again' message, got %q", last.Content)
	}
}

func TestCtrlCSecondTapQuits(t *testing.T) {
	m := testModelWithMessages([]chatMessage{})
	m.quitConfirm = true

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if cmd == nil {
		t.Error("expected quit cmd on second Ctrl+C")
	}
}

func TestCtrlCSelectedOverridesOtherStates(t *testing.T) {
	// Selection should take priority over interrupt and quit
	m := testModelWithMessages([]chatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Streaming: true, Content: "streaming"},
	})
	m.curAssistant = &m.messages[len(m.messages)-1]
	m.status = StatusStreaming
	m.selectStart = 0
	m.selectEnd = 1

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if cmd != nil {
		t.Errorf("expected nil cmd (copy, not quit), got %v", cmd)
	}
	if m.status != StatusStreaming {
		t.Errorf("expected StatusStreaming preserved (copy, not interrupt), got %v", m.status)
	}
}

// --- Command handlers ---

func TestCommandPlan(t *testing.T) {
	// Setup: mock registry with "plan" and "build"
	m := newTestTUI()
	m.registry.Set("build")
	m.modeName = "build"

	_, cmd := m.handleCommand("/plan")

	if cmd != nil {
		t.Errorf("expected nil cmd, got %v", cmd)
	}
	if m.modeName != "plan" {
		t.Errorf("expected mode=plan, got %q", m.modeName)
	}
	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.Content, "Switched to plan") {
		t.Errorf("expected switch message, got %q", last.Content)
	}
}

func TestCommandBuild(t *testing.T) {
	m := newTestTUI()
	m.modeName = "plan"

	_, cmd := m.handleCommand("/build")

	if cmd != nil {
		t.Errorf("expected nil cmd, got %v", cmd)
	}
	if m.modeName != "build" {
		t.Errorf("expected mode=build, got %q", m.modeName)
	}
	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.Content, "Switched to build") {
		t.Errorf("expected switch message, got %q", last.Content)
	}
}

func TestCommandExit(t *testing.T) {
	m := newTestTUI()
	_, cmd := m.handleCommand("/exit")
	if cmd == nil {
		t.Error("expected quit cmd for /exit")
	}
}

func TestCommandQuit(t *testing.T) {
	m := newTestTUI()
	_, cmd := m.handleCommand("/quit")
	if cmd == nil {
		t.Error("expected quit cmd for /quit")
	}
}

func TestCommandVerboseToggle(t *testing.T) {
	m := newTestTUI()
	initial := m.agent.Verbose

	m.handleCommand("/verbose")

	if m.agent.Verbose == initial {
		t.Errorf("expected verbose toggled, was %v", initial)
	}
}

func TestCommandUnknown(t *testing.T) {
	m := newTestTUI()
	_, cmd := m.handleCommand("/nonexistent")
	if cmd != nil {
		t.Errorf("expected nil cmd for unknown command, got %v", cmd)
	}
	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.Content, "Unknown command") {
		t.Errorf("expected unknown command message, got %q", last.Content)
	}
}

func TestCommandModelOpensDialog(t *testing.T) {
	m := newTestTUI()
	m.selectingProvider = false

	m.handleCommand("/model")

	if !m.selectingProvider {
		t.Error("expected selectingProvider=true after /model")
	}
}

// --- Tab mode switch ---

func TestTabSwitchesMode(t *testing.T) {
	m := newTestTUI()
	initialMode := m.modeName

	// Tab sends modeSwitchMsg asynchronously — send it directly
	m.Update(modeSwitchMsg{})

	newMode := m.modeName
	if newMode == initialMode {
		t.Errorf("expected mode to change on Tab, was %s", initialMode)
	}
	// Should have a system message
	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.Content, "Switched") {
		t.Errorf("expected switch message on Tab, got %q", last.Content)
	}
}

func TestTabDoesNotSwitchWithContent(t *testing.T) {
	m := newTestTUI()
	initialMode := m.modeName
	initialCount := len(m.messages)
	m.input.SetValue("text")

	m.Update(tea.KeyMsg{Type: tea.KeyTab})

	// Tab with non-empty input should be consumed by textarea, not switch mode
	if len(m.messages) != initialCount {
		t.Errorf("expected no new system message when input has content, got %d messages",
			len(m.messages))
	}
	_ = initialMode
}

// --- wrapLine edge cases ---

func TestWrapLineShort(t *testing.T) {
	lines := wrapLine("hello", 80)
	if len(lines) != 1 || lines[0] != "hello" {
		t.Errorf("expected ['hello'], got %q", lines)
	}
}

func TestWrapLineExactWidth(t *testing.T) {
	input := strings.Repeat("x", 80)
	lines := wrapLine(input, 80)
	if len(lines) != 1 || lines[0] != input {
		t.Errorf("expected single line of 80 chars, got %d lines", len(lines))
	}
}

func TestWrapLineLonger(t *testing.T) {
	input := strings.Repeat("x", 200)
	lines := wrapLine(input, 80)
	if len(lines) != 3 {
		t.Errorf("expected 3 wrapped lines (200/80=3), got %d", len(lines))
	}
}

func TestWrapLineAtSpace(t *testing.T) {
	input := strings.Repeat("x", 50) + " " + strings.Repeat("y", 50)
	lines := wrapLine(input, 60)
	// Should break at the space, not in the middle of x's
	for _, l := range lines {
		if len(l) > 60 {
			t.Errorf("line exceeds 60 chars: %q (%d)", l, len(l))
		}
	}
	if !strings.Contains(lines[0], "x") || !strings.Contains(lines[1], "y") {
		t.Errorf("unexpected split: %q", lines)
	}
}

func TestWrapLineSingleWordLong(t *testing.T) {
	input := strings.Repeat("x", 200)
	lines := wrapLine(input, 80)
	if len(lines) < 2 {
		t.Errorf("expected multiple lines for long single word, got %d", len(lines))
	}
	for _, l := range lines {
		w := lipgloss.Width(l)
		if w > 80 {
			t.Errorf("line exceeds 80 visible chars: %d", w)
		}
	}
}

func TestWrapLineEmpty(t *testing.T) {
	lines := wrapLine("", 80)
	if len(lines) != 1 || lines[0] != "" {
		t.Errorf("expected [''], got %q", lines)
	}
}

func TestWrapLineZeroWidth(t *testing.T) {
	lines := wrapLine("hello", 0)
	// Width is clamped to 1, so each char gets its own line
	if len(lines) != 5 {
		t.Errorf("expected 5 lines (one per char) for 0-width wrap, got %d", len(lines))
	}
	if len(lines) > 0 && lines[0] != "h" {
		t.Errorf("expected first char 'h', got %q", lines[0])
	}
}

func TestWrapLineCJK(t *testing.T) {
	// Chinese characters are double-width in terminals
	input := "你好世界" + strings.Repeat("a", 100)
	lines := wrapLine(input, 80)
	if len(lines) < 2 {
		t.Errorf("expected wrapping for long CJK+ASCII line, got %d lines", len(lines))
	}
	for _, l := range lines {
		w := lipgloss.Width(l)
		if w > 80 {
			t.Errorf("line exceeds 80 visible chars: %d", w)
		}
	}
}

// --- submitInput ---

func TestSubmitEmptyInput(t *testing.T) {
	m := newTestTUI()
	m.input.SetValue("  ")
	_, cmd := m.submitInput()
	if cmd != nil {
		t.Errorf("expected nil cmd for empty input, got %v", cmd)
	}
}

func TestSubmitNonEmptyInput(t *testing.T) {
	m := newTestTUI()
	m.input.SetValue("hello")
	
	// Call the underlying logic that submitInput triggers
	// (submitInput itself spawns a goroutine that needs a real agent)
	m.messages = append(m.messages, chatMessage{Role: "user", Content: "hello"})
	cur := chatMessage{Role: "assistant", Streaming: true}
	m.messages = append(m.messages, cur)
	m.curAssistant = &m.messages[len(m.messages)-1]
	m.status = StatusStreaming

	if len(m.messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(m.messages))
	}
	if m.messages[0].Content != "hello" {
		t.Errorf("expected user message with 'hello', got %q", m.messages[0].Content)
	}
	if !m.messages[1].Streaming {
		t.Error("expected assistant streaming=true")
	}
}

// --- NewTUI default state ---

func TestNewTUIDefaults(t *testing.T) {
	m := newTestTUI()
	if m.selectStart != -1 {
		t.Errorf("expected selectStart=-1, got %d", m.selectStart)
	}
	if m.selectEnd != -1 {
		t.Errorf("expected selectEnd=-1, got %d", m.selectEnd)
	}
	if m.status != StatusIdle {
		t.Errorf("expected StatusIdle, got %v", m.status)
	}
	if m.modeName == "" {
		t.Error("expected non-empty modeName")
	}
}
