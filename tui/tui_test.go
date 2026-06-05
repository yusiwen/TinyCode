package tui

import (
	"testing"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	tea "github.com/charmbracelet/bubbletea"
)

// newTestTUI creates a TUI model with minimal valid dependencies.
func newTestTUI() *TuiModel {
	return NewTUI(agent.New(nil), &config.Config{}, agent.NewRegistry(),
		agent.NewProviderRegistry([]agent.ProviderRecord{
			{Name: "test", Provider: &agent.MockProvider{}},
		}))
}

func TestInitialInputHeight(t *testing.T) {
	m := newTestTUI()
	if h := m.input.Height(); h != 1 {
		t.Fatalf("expected initial height 1, got %d", h)
	}
}

func TestHeightAfterOneLine(t *testing.T) {
	m := newTestTUI()
	// Simulate typing "hello"
	m.input.SetValue("hello")
	m.adjustInputHeight()
	if h := m.input.Height(); h != 1 {
		t.Fatalf("expected height 1 for 1 line, got %d", h)
	}
}

func TestHeightAfterNewline(t *testing.T) {
	m := newTestTUI()
	m.input.SetValue("hello\n")
	m.adjustInputHeight()
	if h := m.input.Height(); h != 2 {
		t.Fatalf("expected height 2 for 2 lines, got %d", h)
	}
}

func TestHeightAfterMultipleNewlines(t *testing.T) {
	m := newTestTUI()
	m.input.SetValue("line1\nline2\nline3")
	m.adjustInputHeight()
	if h := m.input.Height(); h != 3 {
		t.Fatalf("expected height 3 for 3 lines, got %d", h)
	}
}

func TestHeightCappedAtMax(t *testing.T) {
	m := newTestTUI()
	var long string
	for i := 0; i < 15; i++ {
		long += "line\n"
	}
	m.input.SetValue(long)
	m.adjustInputHeight()
	if h := m.input.Height(); h > maxInputHeight {
		t.Fatalf("expected height <= %d, got %d", maxInputHeight, h)
	}
	if h := m.input.Height(); h != maxInputHeight {
		t.Fatalf("expected height %d for 15 lines, got %d", maxInputHeight, h)
	}
}

func TestHeightResetsAfterSubmit(t *testing.T) {
	m := newTestTUI()
	m.input.SetValue("multi\nline\ntext")
	m.adjustInputHeight()

	// Submit
	m.submitInput()

	// Height should be back to 1 (empty input)
	m.adjustInputHeight()
	if h := m.input.Height(); h != 1 {
		t.Fatalf("expected height 1 after submit, got %d", h)
	}
}

func TestHeightNeverBelowOne(t *testing.T) {
	m := newTestTUI()
	m.input.SetValue("")
	m.adjustInputHeight()
	if h := m.input.Height(); h != 1 {
		t.Fatalf("expected height 1 for empty input, got %d", h)
	}
}

func TestHeightIncreasesOnlyWhenNeeded(t *testing.T) {
	m := newTestTUI()
	// Simulate: type "a" → height stays 1
	m.input.SetValue("a")
	m.adjustInputHeight()
	if h := m.input.Height(); h != 1 {
		t.Fatalf("step1: expected 1, got %d", h)
	}
	// Add more text on same line → height stays 1
	m.input.SetValue("abc")
	m.adjustInputHeight()
	if h := m.input.Height(); h != 1 {
		t.Fatalf("step2: expected 1, got %d", h)
	}
	// Add newline → height becomes 2
	m.input.SetValue("abc\n")
	m.adjustInputHeight()
	if h := m.input.Height(); h != 2 {
		t.Fatalf("step3: expected 2, got %d", h)
	}
	// Add more on same line → height stays 2
	m.input.SetValue("abc\ndef")
	m.adjustInputHeight()
	if h := m.input.Height(); h != 2 {
		t.Fatalf("step4: expected 2, got %d", h)
	}
}

func TestSubmitInputAndCheckContent(t *testing.T) {
	m := newTestTUI()
	m.input.SetValue("hello")
	m.adjustInputHeight()

	// Need to set up m.lastInput — submitInput does this
	m.submitInput()

	if m.lastInput != "hello" {
		t.Fatalf("expected lastInput 'hello', got %q", m.lastInput)
	}
	if v := m.input.Value(); v != "" {
		t.Fatalf("expected input cleared after submit, got %q", v)
	}
}

var _ tea.Model = (*TuiModel)(nil)
