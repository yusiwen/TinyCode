package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/lsp"
)

// lspDiagCmd returns a Bubble Tea command that reads the lsp package's
// in-memory diagnostics snapshot and delivers it as an LSPDiagMsg. Reading the
// registry is a mutex-guarded map copy with no LSP I/O, and it happens in the
// command goroutine, so Update never blocks on diagnostics.
//
// Returning nil when no source is configured keeps tests that build a bare
// model working unchanged.
func (m *TuiModel) lspDiagCmd() tea.Cmd {
	read := m.diagSource
	if read == nil {
		return nil
	}
	return func() tea.Msg {
		return lspDiagMsgFromInfo(read())
	}
}

// applyDiagInfo copies a diagnostics snapshot into the fields rendered by the
// status bar and /diagnostics.
func (m *TuiModel) applyDiagInfo(info lsp.DiagnosticsInfo) {
	m.diagTotal = info.Errors
	m.diagFiles = info.Files
	m.diagFile = info.LastPath
	m.diagDetails = copyDiagDetails(info.Details)
}

// lspDiagMsgFromInfo converts a registry snapshot into an LSPDiagMsg.
func lspDiagMsgFromInfo(info lsp.DiagnosticsInfo) LSPDiagMsg {
	return LSPDiagMsg{
		FilePath: info.LastPath,
		Count:    info.Errors,
		Files:    info.Files,
		Details:  copyDiagDetails(info.Details),
	}
}

// copyDiagDetails clones a details slice so the model never aliases the
// registry's internal data across goroutines.
func copyDiagDetails(details []string) []string {
	if len(details) == 0 {
		return nil
	}
	return append([]string(nil), details...)
}

// diagnosticsListing renders the /diagnostics body: a header plus one line per
// affected file. It returns "" when there are no errors.
func (m *TuiModel) diagnosticsListing() string {
	if m.diagTotal == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("LSP diagnostics: %d error(s) in %d file(s)\n", m.diagTotal, m.diagFiles))
	for _, line := range m.diagDetails {
		b.WriteString("  " + line + "\n")
	}
	return b.String()
}
