package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestThemeDefaultApplied(t *testing.T) {
	if ResponseLabel.Fg != lipgloss.Color("#FFD700") {
		t.Errorf("expected ResponseLabel #FFD700, got %v", ResponseLabel.Fg)
	}
	if !ResponseLabel.Bold {
		t.Error("expected ResponseLabel bold")
	}
}

func TestThemeNames(t *testing.T) {
	names := ThemeNames()
	if len(names) < 2 {
		t.Errorf("expected at least 2 themes, got %d", len(names))
	}
	hasDefault := false
	hasNord := false
	for _, n := range names {
		if n == "default" {
			hasDefault = true
		}
		if n == "nord" {
			hasNord = true
		}
	}
	if !hasDefault {
		t.Error("expected 'default' in theme names")
	}
	if !hasNord {
		t.Error("expected 'nord' in theme names")
	}
}

func TestLookupTheme(t *testing.T) {
	t1 := LookupTheme("nord")
	if t1 == nil {
		t.Fatal("expected nord theme to exist")
	}
	if t1.Name != "nord" {
		t.Errorf("expected name 'nord', got %q", t1.Name)
	}
	t2 := LookupTheme("nonexistent")
	if t2 != nil {
		t.Errorf("expected nil for unknown theme, got %v", t2)
	}
}

func TestThemeSwitchChangesStyles(t *testing.T) {
	// Switch to nord
	ApplyTheme(ThemeNord)
	if ResponseLabel.Fg != lipgloss.Color("#88C0D0") {
		t.Errorf("expected ResponseLabel nord #88C0D0, got %v", ResponseLabel.Fg)
	}
	if DimStyle.Fg != lipgloss.Color("#4C566A") {
		t.Errorf("expected DimStyle nord #4C566A, got %v", DimStyle.Fg)
	}
	// Switch back to default
	ApplyTheme(ThemeDefault)
	if ResponseLabel.Fg != lipgloss.Color("#FFD700") {
		t.Errorf("expected ResponseLabel default #FFD700, got %v", ResponseLabel.Fg)
	}
}

func TestThemeSwitchClearsCache(t *testing.T) {
	// Build a style in the cache
	styleToLipgloss(CellStyle{Fg: lipgloss.Color("#FFD700"), Bold: true})
	// Switch theme (clears cache)
	ApplyTheme(ThemeNord)
	// Verify the cache was cleared
	styleMu.RLock()
	count := len(styleCache)
	styleMu.RUnlock()
	if count != 0 {
		t.Errorf("expected empty style cache after theme switch, got %d entries", count)
	}
	ApplyTheme(ThemeDefault)
}

func TestThemeCommand(t *testing.T) {
	m := newTestTUI()
	// First: /theme (list)
	_, cmd := m.handleCommand("/theme")
	if cmd != nil {
		t.Errorf("expected nil cmd for theme list, got %v", cmd)
	}
	last := m.messages[len(m.messages)-1]
	t.Logf("after /theme: %q", last.Content)
	if !strings.Contains(last.Content, "nord") {
		t.Errorf("expected theme list to include 'nord', got %q", last.Content)
	}

	// Second: /theme nord
	_, cmd = m.handleCommand("/theme nord")
	if cmd != nil {
		t.Errorf("expected nil cmd for theme switch, got %v", cmd)
	}
	last2 := m.messages[len(m.messages)-1]
	t.Logf("after /theme nord: %q", last2.Content)
	
	// Check theme was applied via command handler
	if ResponseLabel.Fg != lipgloss.Color("#88C0D0") {
		t.Errorf("expected nord ResponseLabel after /theme nord, got %v", ResponseLabel.Fg)
	}
	// Reset
	ApplyTheme(ThemeDefault)
}

func TestThemeUnknownCommand(t *testing.T) {
	m := newTestTUI()
	_, cmd := m.handleCommand("/theme doesntexist")
	if cmd != nil {
		t.Errorf("expected nil cmd, got %v", cmd)
	}
	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.Content, "Unknown theme") {
		t.Errorf("expected 'Unknown theme' message, got %q", last.Content)
	}
}
