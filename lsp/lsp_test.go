package lsp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupDemoProject creates a minimal Go project in t.TempDir() for LSP tests.
func setupDemoProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module demo\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	return dir
}

// TestTouchFileNoDiag verifies that a fire-and-forget touch (no diagnostics)
// completes without error.
func TestTouchFileNoDiag(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping LSP integration test in short mode")
	}
	if os.Getenv("LSP_TEST") == "" {
		t.Skip("skipping: set LSP_TEST=1 to run LSP integration tests")
	}
	proj := setupDemoProject(t)

	Init(proj)
	// LSP starts lazily on first TouchFile call

	// Fire-and-forget touch (no diagnostics)
	diags, err := TouchFile(filepath.Join(proj, "main.go"), false)
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
	proj := setupDemoProject(t)

	Init(proj)
	// LSP starts lazily on first TouchFile call

	// Touch with diagnostics — main.go is valid Go, should have no errors
	diags, err := TouchFile(filepath.Join(proj, "main.go"), true)
	if err != nil {
		t.Fatalf("TouchFile (with diag) failed: %v", err)
	}

	t.Logf("Got %d diagnostics for main.go", len(diags))
	for _, d := range diags {
		t.Logf("  [sev=%d] %s", d.Severity, d.Message)
	}
	// main.go is valid Go and must not report errors. Before the client synced
	// the real content, gopls analysed an empty buffer and answered with the
	// phantom error "expected ';', found 'EOF'".
	assertNoErrors(t, "main.go", diags)
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
	proj := setupDemoProject(t)

	// The file must live where the Go toolchain loads it: directories starting
	// with "." are ignored, so an earlier version of this test wrote it into
	// .lsp_test/ and could never observe a diagnostic for it.
	errFile := filepath.Join(proj, "broken.go")
	defer os.Remove(errFile)
	// A helper (not another main) so the only error is the undefined symbol:
	// the demo project's main.go already declares func main.
	badContent := `package main

func helper() {
	undefinedFunc()
}
`
	if err := os.WriteFile(errFile, []byte(badContent), 0644); err != nil {
		t.Fatalf("write broken.go: %v", err)
	}

	Init(proj)
	// LSP starts lazily on first TouchFile call

	diags := waitForErrors(t, errFile, true)
	t.Logf("Got %d diagnostics for broken.go:", len(diags))

	mentioned := false
	for _, d := range diags {
		t.Logf("  [sev=%d] %s", d.Severity, d.Message)
		if d.Severity == 1 && strings.Contains(d.Message, "undefined") {
			mentioned = true
		}
	}
	if !mentioned {
		t.Errorf("expected an undefined-symbol error in broken.go, got: %+v", diags)
	}
}

// TestTouchFileAfterFixClearsErrors exercises the didChange path: after the file
// is repaired and touched again, the error must disappear. Without the change
// notification the server kept analysing the text it first received.
func TestTouchFileAfterFixClearsErrors(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping LSP integration test in short mode")
	}
	if os.Getenv("LSP_TEST") == "" {
		t.Skip("skipping: set LSP_TEST=1 to run LSP integration tests")
	}
	proj := setupDemoProject(t)

	file := filepath.Join(proj, "editable.go")
	defer os.Remove(file)
	if err := os.WriteFile(file, []byte("package main\n\nfunc helper() {\n\tundefinedFunc()\n}\n"), 0644); err != nil {
		t.Fatalf("write editable.go: %v", err)
	}

	Init(proj)
	waitForErrors(t, file, true)

	if err := os.WriteFile(file, []byte("package main\n\nfunc helper() {\n\tprintln(\"ok\")\n}\n"), 0644); err != nil {
		t.Fatalf("rewrite editable.go: %v", err)
	}
	waitForErrors(t, file, false)
}

// assertNoErrors fails when any severity-1 diagnostic is present.
func assertNoErrors(t *testing.T, name string, diags []Diagnostic) {
	t.Helper()
	for _, d := range diags {
		if d.Severity == 1 {
			t.Errorf("%s should be clean, got error: %s", name, d.Message)
		}
	}
}

// waitForErrors polls TouchFile until the file's diagnostics do (want=true) or do
// not (want=false) contain a severity-1 error, and returns them. The server
// publishes asynchronously, so a single call is not enough.
func waitForErrors(t *testing.T, path string, want bool) []Diagnostic {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var last []Diagnostic
	for time.Now().Before(deadline) {
		diags, err := TouchFile(path, true)
		if err != nil {
			t.Fatalf("TouchFile(%s): %v", path, err)
		}
		last = diags
		hasError := false
		for _, d := range diags {
			if d.Severity == 1 {
				hasError = true
				break
			}
		}
		if hasError == want {
			return diags
		}
		time.Sleep(100 * time.Millisecond)
	}
	if want {
		t.Fatalf("expected an error for %s, never got one (last: %+v)", path, last)
	}
	t.Fatalf("expected %s to be clean, still reporting: %+v", path, last)
	return nil
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
		{Severity: 2, Message: "unused variable"},     // WARN
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
