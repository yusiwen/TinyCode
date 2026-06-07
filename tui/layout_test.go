package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
)

func layoutModel(termHeight int) *TuiModel {
	m := &TuiModel{
		ready:        true,
		height:       termHeight,
		width:        80,
		status:       StatusIdle,
		messages:     []chatMessage{},
		selectStart: -1,
		selectEnd:   -1,
	}
	m.vp = viewport.New(80, termHeight-2)
	m.spinner = spinner.New()
	m.spinner.Style = spinnerStyle
	m.input = textarea.New()
	m.input.SetHeight(1)
	return m
}

func countLines(v string) int {
	n := 0
	for _, c := range v {
		if c == '\n' {
			n++
		}
	}
	if len(v) > 0 && v[len(v)-1] != '\n' {
		n++
	}
	return n
}

func TestLayoutHeightFillsTerminal(t *testing.T) {
	for _, termH := range []int{25, 30, 40, 50} {
		wanted := 1
		if termH-1-wanted < 1 {
			continue
		}
		m := layoutModel(termH)
		m.messages = []chatMessage{{Role: "user", Content: "hello"}}
		m.vp.Height = m.height - 1 - wanted

		v := m.View()
		lines := countLines(v)

		if lines < termH-1 || lines > termH+1 {
			t.Errorf("height=%d: got %d lines (expected ~%d)",
				termH, lines, termH)
		}
	}
}

func TestLayoutHeightStreaming(t *testing.T) {
	for _, termH := range []int{25, 30} {
		m := layoutModel(termH)
		m.status = StatusStreaming
		m.messages = []chatMessage{{Role: "assistant", Streaming: true, Content: "thinking..."}}
		m.vp.Height = m.height - 1 - 1

		v := m.View()
		lines := countLines(v)

		if lines < termH-1 || lines > termH+1 {
			t.Errorf("streaming height=%d: got %d lines (expected ~%d)", termH, lines, termH)
		}
	}
}

func TestLayoutHeightProviderDialog(t *testing.T) {
	for _, termH := range []int{25, 30} {
		m := layoutModel(termH)
		m.selectingProvider = true
		m.providerCursor = 0
		m.vp.Height = m.height - 1 - 1

		v := m.View()
		lines := countLines(v)

		if lines > termH+1 {
			t.Errorf("provider dialog height=%d: got %d lines (expected ~%d)", termH, lines, termH)
		}
	}
}

func TestLayoutFormulaApplied(t *testing.T) {
	for termH := 30; termH <= 100; termH += 20 {
		for _, wanted := range []int{1, 3, 5} {
			expectedVP := termH - 1 - wanted
			if expectedVP < 1 {
				continue
			}
			m := layoutModel(termH)
			lines := make([]string, wanted)
			for i := range lines {
				lines[i] = "x"
			}
			m.input.SetValue(strings.Join(lines, "\n"))
			m.adjustInputHeight()

			if m.vp.Height != expectedVP {
				t.Errorf("height=%d wanted=%d: expected vp=%d, got vp=%d",
					termH, wanted, expectedVP, m.vp.Height)
			}
		}
	}
}

func TestStatusBarShowsMode(t *testing.T) {
	m := layoutModel(30)
	m.modeName = "build"
	bar := m.renderStatusBar()
	if !strings.Contains(bar, "build") {
		t.Errorf("status bar missing mode: %q", bar)
	}
}

func TestStatusBarShowsTokens(t *testing.T) {
	m := layoutModel(30)
	m.sessionTokens = 1234
	bar := m.renderStatusBar()
	if !strings.Contains(bar, "1234") {
		t.Errorf("status bar missing tokens: %q", bar)
	}
}
