package tui

import (
	"strings"
	"testing"
)

func TestSkillCommandList(t *testing.T) {
	m := newTestTUI()
	m.ready = true

	_, cmd := m.handleCommand("/skill")
	if cmd != nil {
		t.Errorf("expected nil cmd for skill list, got %v", cmd)
	}
	// Should show usage in status bar
	if !strings.Contains(m.statusMsg, "code-review") {
		t.Errorf("expected code-review in skill list, got %q", m.statusMsg)
	}
}

func TestSkillCommandLoad(t *testing.T) {
	m := newTestTUI()
	m.ready = true

	_, cmd := m.handleCommand("/skill code-review")
	if cmd != nil {
		t.Errorf("expected nil cmd for skill load, got %v", cmd)
	}
	// Should add a system message with the skill content
	if len(m.messages) == 0 {
		t.Fatal("expected skill content to be added as message")
	}
	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.Content, "code-review") {
		t.Errorf("expected code-review in loaded skill, got %q", last.Content)
	}
	if !strings.Contains(last.Content, "Steps") {
		t.Errorf("expected Steps section in loaded skill, got %q", last.Content)
	}
}

func TestSkillCommandNotFound(t *testing.T) {
	m := newTestTUI()
	m.ready = true

	_, cmd := m.handleCommand("/skill nonexistent")
	if cmd != nil {
		t.Errorf("expected nil cmd for not found, got %v", cmd)
	}
	if !strings.Contains(m.statusMsg, "not found") {
		t.Errorf("expected 'not found' in status, got %q", m.statusMsg)
	}
}
