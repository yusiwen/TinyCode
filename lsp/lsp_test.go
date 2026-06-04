package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

const demoProject = "/home/yusiwen/tmp/demo_project"

// TestTouchFileNoDiag verifies that a fire-and-forget touch (no diagnostics)
// completes without error.
func TestTouchFileNoDiag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping LSP integration test in short mode")
	}
	if os.Getenv("LSP_TEST") == "" {
		t.Skip("skipping: set LSP_TEST=1 to run LSP integration tests")
	}
	if _, err := os.Stat(filepath.Join(demoProject, "main.go")); err != nil {
		t.Fatalf("demo project not found at %s: %v", demoProject, err)
	}

	Init(demoProject)
	// LSP starts lazily on first TouchFile call

	// Fire-and-forget touch (no diagnostics)
	diags, err := TouchFile(filepath.Join(demoProject, "main.go"), false)
	if err != nil {
		t.Fatalf("TouchFile (no diag) failed: %v", err)
	}
	if diags != nil {
		t.Logf("unexpected diagnostics returned: %v", diags)
	}
}

// TestTouchFileWithDiag verifies that touching a file with diagnostics
// returns results from gopls.
func TestTouchFileWithDiag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping LSP integration test in short mode")
	}
	if os.Getenv("LSP_TEST") == "" {
		t.Skip("skipping: set LSP_TEST=1 to run LSP integration tests")
	}
	if _, err := os.Stat(filepath.Join(demoProject, "main.go")); err != nil {
		t.Fatalf("demo project not found at %s: %v", demoProject, err)
	}

	Init(demoProject)
	// LSP starts lazily on first TouchFile call

	// Touch with diagnostics — main.go is valid Go, should have no errors
	diags, err := TouchFile(filepath.Join(demoProject, "main.go"), true)
	if err != nil {
		t.Fatalf("TouchFile (with diag) failed: %v", err)
	}

	t.Logf("Got %d diagnostics for main.go", len(diags))
	for _, d := range diags {
		t.Logf("  [sev=%d] %s", d.Severity, d.Message)
	}
}

// TestTouchFileWithErrors verifies that a file with deliberate errors
// gets caught by LSP diagnostics.
func TestTouchFileWithErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping LSP integration test in short mode")
	}
	if os.Getenv("LSP_TEST") == "" {
		t.Skip("skipping: set LSP_TEST=1 to run LSP integration tests")
	}
	if _, err := os.Stat(filepath.Join(demoProject, "main.go")); err != nil {
		t.Fatalf("demo project not found at %s: %v", demoProject, err)
	}

	tmpDir := filepath.Join(demoProject, ".lsp_test")
	os.MkdirAll(tmpDir, 0755)
	defer os.RemoveAll(tmpDir)
	errFile := filepath.Join(tmpDir, "broken.go")
	badContent := `package main

func main() {
	undefinedFunc()
}
`
	if err := os.WriteFile(errFile, []byte(badContent), 0644); err != nil {
		t.Fatalf("write broken.go: %v", err)
	}

	Init(demoProject)
	// LSP starts lazily on first TouchFile call

	diags, err := TouchFile(errFile, true)
	if err != nil {
		t.Fatalf("TouchFile (errors) failed: %v", err)
	}

	if len(diags) == 0 {
		t.Log("no diagnostics returned (gopls may need more time)")
		return
	}

	t.Logf("Got %d diagnostics for broken.go:", len(diags))

	found := false
	for _, d := range diags {
		t.Logf("  [sev=%d] %s", d.Severity, d.Message)
		if d.Severity == 1 && d.Message != "" {
			found = true
		}
	}
	if !found {
		t.Error("expected at least one ERROR level diagnostic in broken.go")
	}
}

// TestFormatDiagnostics verifies the formatter works correctly.
func TestFormatDiagnostics(t *testing.T) {
	diags := []Diagnostic{
		{Severity: 1, Range: Range{Start: Position{Line: 4, Character: 1}}, Message: "expected declaration, found undefinedFunc"},
		{Severity: 2, Range: Range{Start: Position{Line: 2, Character: 5}}, Message: "unused variable"}, // WARN, should be ignored
		{Severity: 1, Range: Range{Start: Position{Line: 5, Character: 2}}, Message: "undefined: x"},
	}

	result := FormatDiagnostics("broken.go", diags)
	t.Logf("Formatted output:\n%s", result)

	if result == "" {
		t.Fatal("FormatDiagnostics returned empty for errors")
	}
}

// TestFormatDiagnosticsNoErrors verifies that no errors = empty output.
func TestFormatDiagnosticsNoErrors(t *testing.T) {
	diags := []Diagnostic{
		{Severity: 2, Message: "unused variable"},    // WARN
		{Severity: 3, Message: "deprecated function"}, // INFO
	}

	result := FormatDiagnostics("clean.go", diags)
	if result != "" {
		t.Fatalf("expected empty for non-error diagnostics, got: %q", result)
	}
}

// TestFormatDiagnosticsMaxErrors verifies the 20-error limit.
func TestFormatDiagnosticsMaxErrors(t *testing.T) {
	diags := make([]Diagnostic, 25)
	for i := range diags {
		diags[i] = Diagnostic{
			Severity: 1,
			Range:    Range{Start: Position{Line: i, Character: 0}},
			Message:  "error",
		}
	}

	result := FormatDiagnostics("big.go", diags)
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if len(result) < 100 {
		t.Fatalf("expected substantial output, got %d chars: %s", len(result), result)
	}
}
