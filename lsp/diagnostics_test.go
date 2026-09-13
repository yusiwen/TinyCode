package lsp

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestDiagnosticsRegistry verifies that diagnostics pushed by the LSP server
// are tracked per file, that warnings are excluded from the error count, and
// that a file's count drops once its errors are fixed.
func TestDiagnosticsRegistry(t *testing.T) {
	resetDiagnostics()
	t.Cleanup(resetDiagnostics)

	mock, conn := newMockLSP()
	defer mock.close()

	client := NewClient(conn)
	if err := client.Initialize("file:///test"); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	uriA := "file:///test/a.go"
	uriB := "file:///test/b.go"
	mock.addDiag(uriA, []Diagnostic{
		{Severity: 1, Range: Range{Start: Position{Line: 2, Character: 4}}, Message: "undefined: x"},
		{Severity: 1, Range: Range{Start: Position{Line: 7, Character: 0}}, Message: "expected ';'"},
		{Severity: 2, Range: Range{Start: Position{Line: 1, Character: 0}}, Message: "unused variable"},
	})
	mock.addDiag(uriB, []Diagnostic{
		{Severity: 1, Range: Range{Start: Position{Line: 0, Character: 1}}, Message: "cannot find package"},
	})

	// Feed diagnostics for both files through the didOpen -> publishDiagnostics
	// flow, which is what TouchFile/GetNewDiagnostics rely on.
	if _, err := client.Diagnostics(uriA, "package a"); err != nil {
		t.Fatalf("diagnostics a: %v", err)
	}
	if _, err := client.Diagnostics(uriB, "package b"); err != nil {
		t.Fatalf("diagnostics b: %v", err)
	}

	files, errors := DiagnosticsSummary()
	if files != 2 || errors != 3 {
		t.Fatalf("summary = (%d files, %d errors), want (2, 3)", files, errors)
	}

	details := DiagnosticsDetails()
	if len(details) != 2 {
		t.Fatalf("details has %d lines, want 2: %v", len(details), details)
	}
	// Details are ordered by path, so a.go comes first.
	pathA := filepath.FromSlash("/test/a.go")
	pathB := filepath.FromSlash("/test/b.go")
	if !strings.HasPrefix(details[0], pathA) || !strings.Contains(details[0], "2 error(s)") {
		t.Errorf("details[0] = %q, want the a.go line with 2 error(s)", details[0])
	}
	if !strings.Contains(details[0], "undefined: x") {
		t.Errorf("details[0] = %q, want the a.go error message", details[0])
	}
	if !strings.HasPrefix(details[1], pathB) || !strings.Contains(details[1], "1 error(s)") {
		t.Errorf("details[1] = %q, want the b.go line with 1 error(s)", details[1])
	}

	// Fix a.go: the server publishes an empty diagnostic set for it.
	mock.addDiag(uriA, nil)
	if _, err := client.Diagnostics(uriA, "package a"); err != nil {
		t.Fatalf("diagnostics a (fixed): %v", err)
	}

	files, errors = DiagnosticsSummary()
	if files != 1 || errors != 1 {
		t.Fatalf("summary after fix = (%d files, %d errors), want (1, 1)", files, errors)
	}
	details = DiagnosticsDetails()
	if len(details) != 1 || !strings.HasPrefix(details[0], pathB) {
		t.Fatalf("details after fix = %v, want only the b.go line", details)
	}
}

// TestDiagnosticsRegistryFiltersWarnings documents that the registry is
// error-only: a file with warnings but no errors never appears.
func TestDiagnosticsRegistryFiltersWarnings(t *testing.T) {
	resetDiagnostics()
	t.Cleanup(resetDiagnostics)

	recordDiagnostics("/tmp/warn.go", []Diagnostic{
		{Severity: 2, Message: "unused"},
	})
	if files, errors := DiagnosticsSummary(); files != 0 || errors != 0 {
		t.Fatalf("summary = (%d, %d), want (0, 0)", files, errors)
	}
	if details := DiagnosticsDetails(); len(details) != 0 {
		t.Fatalf("details = %v, want empty", details)
	}
}
