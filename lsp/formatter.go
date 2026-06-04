package lsp

import (
	"fmt"
	"strings"
)

const maxErrorsPerFile = 20

// FormatDiagnostics formats LSP diagnostics into LLM-readable output.
// Only ERROR level (severity == 1), max 20 errors.
// Returns empty string if no errors.
func FormatDiagnostics(filePath string, diags []Diagnostic) string {
	// Filter only ERROR level
	var errors []Diagnostic
	for _, d := range diags {
		if d.Severity == 1 {
			errors = append(errors, d)
		}
	}
	if len(errors) == 0 {
		return ""
	}

	// Limit to maxErrors
	limited := errors
	more := 0
	if len(limited) > maxErrorsPerFile {
		limited = errors[:maxErrorsPerFile]
		more = len(errors) - maxErrorsPerFile
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n<diagnostics file=\"%s\">\n", filePath))
	for _, e := range limited {
		line := e.Range.Start.Line + 1
		col := e.Range.Start.Character + 1
		sb.WriteString(fmt.Sprintf("ERROR [%d:%d] %s\n", line, col, e.Message))
	}
	if more > 0 {
		sb.WriteString(fmt.Sprintf("... and %d more\n", more))
	}
	sb.WriteString("</diagnostics>")
	return sb.String()
}
