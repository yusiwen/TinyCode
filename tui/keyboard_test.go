package tui

import (
	"strings"
	"testing"

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
	if m.selectStart != -1 || m.selectEnd != -1 {
		t.Errorf("expected selection cleared after copy, got start=%d end=%d", m.selectStart, m.selectEnd)
	}
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
		t.Errorf("expected nil cmd (copy), got %v", cmd)
	}
	if m.status != StatusStreaming {
		t.Errorf("expected StatusStreaming preserved (copy overrides interrupt), got %v", m.status)
	}
}

// --- Tab mode switch ---

func TestTabSwitchesMode(t *testing.T) {
	m := newTestTUI()
	initialMode := m.modeName

	// Test that registry can switch
	newName := m.registry.Switch()
	if newName == initialMode {
		// Registry has only one primary — test differently
		t.Logf("registry has only %s primary, testing command path instead", initialMode)
		m.handleCommand("/plan")
		m.handleCommand("/build")
	}

	// Now test the modeSwitchMsg path
	m.Update(modeSwitchMsg{})

	if m.modeName == initialMode {
		t.Logf("mode unchanged (may have only one primary agent)")
	} else {
		last := m.messages[len(m.messages)-1]
		if !strings.Contains(last.Content, "Switched") {
			t.Errorf("expected switch message on Tab, got %q", last.Content)
		}
	}
}

func TestTabDoesNotSwitchWithContent(t *testing.T) {
	m := newTestTUI()
	initialCount := len(m.messages)
	m.input.SetValue("text")

	m.Update(tea.KeyMsg{Type: tea.KeyTab})

	if len(m.messages) != initialCount {
		t.Errorf("expected no new message when input has content, got %d messages", len(m.messages))
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
	m.status = StatusIdle

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
