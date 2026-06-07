package tui

import "testing"

// NewTUI default state
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
