package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/lsp"
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

// testDiagInfo is a fixed snapshot injected in place of the real lsp registry.
func testDiagInfo() lsp.DiagnosticsInfo {
	return lsp.DiagnosticsInfo{
		Files:    2,
		Errors:   3,
		LastPath: "/tmp/broken.go",
		Details: []string{
			"/tmp/broken.go: 2 error(s) - 3:1 undefined: x; 8:1 expected ';'",
			"/tmp/other.go: 1 error(s) - 1:2 cannot find package",
		},
	}
}

// TestDiagnosticsRefreshAndListing covers the full wiring: the model's tick
// produces an LSPDiagMsg from the diagnostics source, the status bar shows the
// total, and /diagnostics lists the per-file details.
func TestDiagnosticsRefreshAndListing(t *testing.T) {
	m := newTestTUI()
	m.ready = true
	m.diagSource = testDiagInfo

	// The periodic spinner tick must schedule a diagnostics read.
	model, tickCmd := m.Update(spinner.TickMsg{})
	m = model.(*TuiModel)
	if tickCmd == nil {
		t.Fatal("spinner tick returned no command")
	}
	raw := tickCmd()
	batch, ok := raw.(tea.BatchMsg)
	if !ok {
		t.Fatalf("tick command returned %T, want tea.BatchMsg", raw)
	}

	var diagMsg LSPDiagMsg
	found := false
	for _, c := range batch {
		if c == nil {
			continue
		}
		if msg, ok := c().(LSPDiagMsg); ok {
			diagMsg = msg
			found = true
		}
	}
	if !found {
		t.Fatal("tick did not produce an LSPDiagMsg (dead diagnostics feature)")
	}
	if diagMsg.Count != 3 || diagMsg.Files != 2 {
		t.Fatalf("LSPDiagMsg = %+v, want 3 errors in 2 files", diagMsg)
	}

	model, _ = m.Update(diagMsg)
	m = model.(*TuiModel)

	if m.diagTotal != 3 || m.diagFiles != 2 {
		t.Fatalf("model diag = (%d, %d), want (3, 2)", m.diagTotal, m.diagFiles)
	}
	if view := stripANSIView(m.View()); !strings.Contains(view, "errors: 3") {
		t.Errorf("expected 'errors: 3' in status bar, got %q", view)
	}

	// /diagnostics lists the per-file details.
	_, cmd := m.handleCommand("/diagnostics")
	if cmd != nil {
		t.Errorf("expected nil cmd from /diagnostics, got %v", cmd)
	}
	if len(m.messages) == 0 {
		t.Fatal("/diagnostics appended no message")
	}
	last := m.messages[len(m.messages)-1]
	if last.Role != "system" {
		t.Errorf("expected a system message, got role %q", last.Role)
	}
	for _, want := range []string{"3 error(s) in 2 file(s)", "/tmp/broken.go", "2 error(s)", "undefined: x", "/tmp/other.go", "cannot find package"} {
		if !strings.Contains(last.Content, want) {
			t.Errorf("/diagnostics listing missing %q:\n%s", want, last.Content)
		}
	}
}

// TestDiagnosticsNoneWithoutServer verifies the default state stays empty and
// panic-free when no LSP server ever produced diagnostics (the tui test setup).
func TestDiagnosticsNoneWithoutServer(t *testing.T) {
	m := newTestTUI()
	m.ready = true

	cmd := m.lspDiagCmd()
	if cmd == nil {
		t.Fatal("expected a diagnostics command from the default source")
	}
	msg, ok := cmd().(LSPDiagMsg)
	if !ok {
		t.Fatalf("diagnostics command returned %T, want LSPDiagMsg", cmd())
	}
	if msg.Count != 0 || msg.Files != 0 {
		t.Fatalf("expected an empty snapshot, got %+v", msg)
	}

	model, _ := m.Update(msg)
	m = model.(*TuiModel)
	if m.diagTotal != 0 || m.diagFiles != 0 || m.diagFile != "" || len(m.diagDetails) != 0 {
		t.Fatalf("expected zero diagnostics, got total=%d files=%d file=%q details=%v",
			m.diagTotal, m.diagFiles, m.diagFile, m.diagDetails)
	}

	if view := stripANSIView(m.View()); strings.Contains(view, "errors") {
		t.Errorf("expected no errors in status bar, got %q", view)
	}

	// /diagnostics must not panic and must not invent a listing.
	before := len(m.messages)
	_, cmd = m.handleCommand("/diagnostics")
	if cmd != nil {
		t.Errorf("expected nil cmd from /diagnostics, got %v", cmd)
	}
	if len(m.messages) != before {
		t.Errorf("/diagnostics appended %d message(s) with no diagnostics", len(m.messages)-before)
	}
	for _, msg := range m.messages {
		if strings.Contains(msg.Content, "LSP diagnostics:") {
			t.Errorf("unexpected listing message: %q", msg.Content)
		}
	}
}
