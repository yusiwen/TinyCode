package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
)

// stripANSIView removes ANSI escape sequences from a string (for test assertions).
func stripANSIView(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && !(s[i] >= 'A' && s[i] <= 'z' || s[i] >= '@' && s[i] <= '~') {
				i++
			}
			if i < len(s) {
				i++
			}
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

func TestViewRendersUserMessage(t *testing.T) {
	m := &TuiModel{
		ready:    true,
		width:    80,
		height:   40,
		messages: []chatMessage{
			{Role: "user", Content: "Hello from user"},
		},
		selectStart: -1,
		selectEnd:   -1,
		charSelStart: selPos{Offset: -1},
		charSelEnd:   selPos{Offset: -1},
		input:        textarea.New(),
		spinner:      spinner.New(),
		status:       StatusIdle,
		sessionStart: time.Now(),
	}
	m.input.SetWidth(80)
	m.vp = viewport.New(80, 30)

	output := stripANSIView(m.View())

	if len(output) < 10 {
		t.Fatal("View() returned too little content, may not be rendering correctly")
	}
	if !strings.Contains(output, "Hello from user") {
		t.Errorf("expected user message in output, got:\n%s", output)
	}
}

func TestViewRendersAssistantResponse(t *testing.T) {
	m := &TuiModel{
		ready:    true,
		width:    80,
		height:   40,
		messages: []chatMessage{
			{Role: "user", Content: "Hi"},
			{Role: "assistant", Content: "**bold** and `code`"},
		},
		selectStart: -1,
		selectEnd:   -1,
		charSelStart: selPos{Offset: -1},
		charSelEnd:   selPos{Offset: -1},
		input:        textarea.New(),
		spinner:      spinner.New(),
		status:       StatusIdle,
		sessionStart: time.Now(),
	}
	m.input.SetWidth(80)
	m.vp = viewport.New(80, 30)

	output := stripANSIView(m.View())

	checks := []string{
		"Response:",         // label
		"bold",              // rendered bold text
		"code",              // rendered inline code text
	}
	for _, s := range checks {
		if !strings.Contains(output, s) {
			t.Errorf("MISSING in view output: %q\nOutput:\n%s", s, output)
		}
	}
}

func TestViewRendersReasoning(t *testing.T) {
	m := &TuiModel{
		ready:    true,
		width:    80,
		height:   40,
		messages: []chatMessage{
			{Role: "assistant", Content: "Final answer.",
				ReasoningContent: "Step by step thinking.\nMore reasoning."},
		},
		selectStart: -1,
		selectEnd:   -1,
		charSelStart: selPos{Offset: -1},
		charSelEnd:   selPos{Offset: -1},
		input:        textarea.New(),
		spinner:      spinner.New(),
		status:       StatusIdle,
		sessionStart: time.Now(),
	}
	m.input.SetWidth(80)
	m.vp = viewport.New(80, 30)

	output := stripANSIView(m.View())

	checks := []string{
		"[-]",               // reasoning expanded marker
		"Step by step",      // reasoning content
		"More reasoning",    // reasoning content
		"Response:",         // label
		"Final answer.",     // content
	}
	for _, s := range checks {
		if !strings.Contains(output, s) {
			t.Errorf("MISSING in view output: %q\nOutput:\n%s", s, output)
		}
	}
}

func TestViewRendersSystemMessage(t *testing.T) {
	m := &TuiModel{
		ready:    true,
		width:    80,
		height:   40,
		messages: []chatMessage{
			{Role: "system", Content: "Mode switched to plan"},
		},
		selectStart: -1,
		selectEnd:   -1,
		charSelStart: selPos{Offset: -1},
		charSelEnd:   selPos{Offset: -1},
		input:        textarea.New(),
		spinner:      spinner.New(),
		status:       StatusIdle,
		sessionStart: time.Now(),
	}
	m.input.SetWidth(80)
	m.vp = viewport.New(80, 30)

	output := stripANSIView(m.View())

	if !strings.Contains(output, "Mode switched") {
		t.Errorf("expected system message in output, got:\n%s", output)
	}
}

func TestViewRendersStatusBar(t *testing.T) {
	m := &TuiModel{
		ready:    true,
		width:    80,
		height:   40,
		selectStart: -1,
		selectEnd:   -1,
		charSelStart: selPos{Offset: -1},
		charSelEnd:   selPos{Offset: -1},
		input:        textarea.New(),
		spinner:      spinner.New(),
		status:       StatusIdle,
		modeName:     "plan",
		sessionStart: time.Now(),
	}
	m.input.SetWidth(80)
	m.vp = viewport.New(80, 30)

	output := stripANSIView(m.View())

	if !strings.Contains(output, "plan") {
		t.Errorf("expected mode name in status bar, got:\n%s", output)
	}
}
