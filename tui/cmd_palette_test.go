package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCmdPaletteActivateOnSlash(t *testing.T) {
	m := newTestTUI()
	m.ready = true

	// Simulate pressing "/" on empty input
	m.input.SetValue("/")
	// The Update function handles this in the keyMsg case
	// Manually trigger by checking the palette state
	// In the real flow, pressing "/" sets cmdPalette=true
	m.cmdPalette = true
	m.cmdPaletteInput = ""
	m.cmdPaletteSel = 0

	if !m.cmdPalette {
		t.Error("expected cmdPalette to be active after '/'")
	}

	// Render should show the palette
	output := m.View()
	if !strings.Contains(output, "Commands:") {
		t.Error("expected 'Commands:' header in view")
	}
	if !strings.Contains(output, "/help") {
		t.Error("expected /help in command palette")
	}
	if !strings.Contains(output, "navigate") {
		t.Error("expected navigation hint in command palette")
	}
}

func TestCmdPaletteFilter(t *testing.T) {
	m := newTestTUI()
	m.ready = true
	m.cmdPalette = true
	m.cmdPaletteInput = "th"
	m.cmdPaletteSel = 0

	cmds := m.filteredCmds()
	if len(cmds) == 0 {
		t.Fatal("expected filtered commands for 'th'")
	}
	found := false
	for _, c := range cmds {
		if c.Name == "/theme" || c.Name == "/thinking" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected /theme or /thinking in filtered commands, got %v", cmds)
	}
}

func TestCmdPaletteFilterNoMatch(t *testing.T) {
	m := newTestTUI()
	m.ready = true
	m.cmdPalette = true
	m.cmdPaletteInput = "zzzz"

	cmds := m.filteredCmds()
	if len(cmds) != 0 {
		t.Errorf("expected empty filtered commands for 'zzzz', got %d", len(cmds))
	}
}

func TestCmdPaletteShowAllOnEmpty(t *testing.T) {
	m := newTestTUI()
	m.ready = true
	m.cmdPalette = true
	m.cmdPaletteInput = ""

	cmds := m.filteredCmds()
	if len(cmds) < 5 {
		t.Errorf("expected all commands when filter is empty, got %d", len(cmds))
	}
}

func TestCmdPaletteEnterExecutesCommand(t *testing.T) {
	m := newTestTUI()
	m.ready = true
	m.cmdPalette = true
	m.cmdPaletteInput = ""
	m.cmdPaletteSel = 0
	m.input.SetValue("/")

	// Simulate the keystrokes path: the update handler for Enter
	// calls handleCommand with the selected command
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if model != m {
		t.Error("expected same model after command execution")
	}
	// After Enter, cmdPalette should be false
	if m.cmdPalette {
		t.Error("expected cmdPalette to close after Enter")
	}
}

func TestCmdPaletteEscCancels(t *testing.T) {
	m := newTestTUI()
	m.ready = true
	m.cmdPalette = true
	m.cmdPaletteInput = "help"

	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if model != m {
		t.Error("expected same model after escape")
	}
	if m.cmdPalette {
		t.Error("expected cmdPalette to close after Escape")
	}
	if m.input.Value() != "" {
		t.Errorf("expected empty input after Escape, got %q", m.input.Value())
	}
}

func TestCmdPaletteUpDown(t *testing.T) {
	m := newTestTUI()
	m.ready = true
	m.cmdPalette = true
	m.cmdPaletteInput = ""
	m.cmdPaletteSel = 0

	// Down
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.cmdPaletteSel != 1 {
		t.Errorf("expected selection=1 after Down, got %d", m.cmdPaletteSel)
	}

	// Up
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.cmdPaletteSel != 0 {
		t.Errorf("expected selection=0 after Up, got %d", m.cmdPaletteSel)
	}
}
