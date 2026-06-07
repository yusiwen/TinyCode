package tui

import (
	"strings"
	"testing"
)

// --- Command handlers ---

func TestCommandPlan(t *testing.T) {
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

func TestCommandThinkingToggle(t *testing.T) {
	m := newTestTUI()
	initial := false
	if m.config.ShowThinking != nil {
		initial = *m.config.ShowThinking
	}

	m.handleCommand("/thinking")

	if m.config.ShowThinking == nil {
		t.Error("expected ShowThinking to be set after toggle")
	} else if *m.config.ShowThinking == initial {
		t.Errorf("expected ShowThinking toggled, was %v", initial)
	}
}
