package tui

import (
	"strings"
	"testing"
)

func TestLSPDiagMsgUpdatesCount(t *testing.T) {
	m := newTestTUI()
	m.diagTotal = 0
	m.diagFile = ""

	// Simulate receiving a diagnostic message
	model, _ := m.Update(LSPDiagMsg{FilePath: "main.go", Count: 3})

	m2 := model.(*TuiModel)
	if m2.diagTotal != 3 {
		t.Errorf("expected diagTotal=3, got %d", m2.diagTotal)
	}
	if m2.diagFile != "main.go" {
		t.Errorf("expected diagFile=main.go, got %q", m2.diagFile)
	}
}

func TestDiagnosticsStatusBar(t *testing.T) {
	m := newTestTUI()
	m.ready = true
	m.diagTotal = 0

	// No diagnostics → no errors in status bar
	output0 := stripANSIView(m.View())
	if strings.Contains(output0, "errors") {
		t.Errorf("expected no errors in status bar, got %q", output0)
	}

	// With diagnostics → shows error count
	m.diagTotal = 3
	output3 := stripANSIView(m.View())
	if !strings.Contains(output3, "errors: 3") {
		t.Errorf("expected 'errors: 3' in status bar, got %q", output3)
	}
}
