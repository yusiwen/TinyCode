package lsp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestServerStartAndClose covers the standalone server wrapper: it must launch a
// process, expose a client and conn, and tear both down again.
func TestServerStartAndClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test relies on /bin/cat")
	}
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skipf("cat not available: %v", err)
	}

	srv, err := Start(context.Background(), cat)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if srv.Client == nil || srv.Conn == nil {
		t.Fatal("Start returned a server without client/conn")
	}
	if err := srv.Close(); err != nil {
		t.Logf("Close returned %v", err)
	}
	_ = srv.cmd.Wait()
}

// TestServerStartMissingBinary covers the launch-failure branch.
func TestServerStartMissingBinary(t *testing.T) {
	if _, err := Start(context.Background(), "tinycode-no-such-lsp-binary"); err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

// TestLanguageForPath covers the extension → server-language mapping. It is
// deliberately separate from languageIDForPath: the two differ for .tsx/.jsx
// (react languageIds are still served by the typescript server) and for
// extensions no server is configured for.
func TestLanguageForPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"main.go", "go"},
		{"app.py", "python"},
		{"stub.pyi", "python"},
		{"index.ts", "typescript"},
		{"App.tsx", "typescript"},
		{"index.js", "javascript"},
		{"index.mjs", "javascript"},
		{"App.jsx", "javascript"},
		{"main.rs", "rust"},
		{"Main.java", "java"},
		{"main.c", "cpp"},
		{"main.cpp", "cpp"},
		{"MAIN.HPP", "cpp"},
		{"notes.txt", ""},
		{"Makefile", ""},
		{"noext", ""},
	}
	for _, tt := range tests {
		if got := languageForPath(tt.path); got != tt.want {
			t.Errorf("languageForPath(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

// TestServerLanguageProjectMarkerWins verifies that a project marker decides the
// server even when the touched file belongs to another language: a Go module
// keeps using gopls for a stray helper file.
func TestServerLanguageProjectMarkerWins(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module demo\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if got := serverLanguage(dir, filepath.Join(dir, "script.py")); got != "go" {
		t.Fatalf("serverLanguage with go.mod = %q, want %q", got, "go")
	}
}

// TestServerLanguageFallsBackToFileExtension is the regression guard for
// non-Go projects: when the project has no marker file in its root (here a
// TypeScript source in a subdirectory), the file's own extension must select the
// server. The old code fell back to gopls, which then answered "not included in
// your workspace" for every file.
func TestServerLanguageFallsBackToFileExtension(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "src")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	ts := filepath.Join(sub, "main.ts")
	if err := os.WriteFile(ts, []byte("export const x = 1\n"), 0644); err != nil {
		t.Fatalf("write main.ts: %v", err)
	}

	if lang := DetectLanguage(dir); lang != "" {
		t.Fatalf("DetectLanguage(%q) = %q, want empty (no marker, no root sources)", dir, lang)
	}
	got := serverLanguage(dir, ts)
	if got != "typescript" {
		t.Fatalf("serverLanguage = %q, want %q", got, "typescript")
	}
	cfg := FindConfig(got)
	if cfg == nil {
		t.Fatalf("no config for %q", got)
	}
	if cfg.Command == "gopls" {
		t.Fatalf("non-Go file resolved to gopls")
	}
}

// TestServerLanguageUnknownReturnsEmpty pins the "no server" answer for files
// no language server handles. Callers must treat it as an error instead of
// starting gopls.
func TestServerLanguageUnknownReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	if got := serverLanguage(dir, filepath.Join(dir, "notes.txt")); got != "" {
		t.Fatalf("serverLanguage(notes.txt) = %q, want empty", got)
	}
}

// TestTouchFileUnknownLanguageFailsFast verifies that touching a file with no
// configured server fails without spawning any process and without leaving LSP
// marked available. This does not need a real language server, so it runs
// unconditionally.
func TestTouchFileUnknownLanguageFailsFast(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("hello\n"), 0644); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}

	Init(dir)
	defer Init("")

	mu.Lock()
	cmdBefore := serverCmd
	mu.Unlock()
	if cmdBefore != nil {
		t.Fatalf("a server was already running before the touch")
	}

	if _, err := TouchFile(file, true); err == nil {
		t.Fatalf("TouchFile(notes.txt) succeeded, want an error")
	}
	if IsAvailable() {
		t.Fatalf("LSP reported available after a failed start")
	}
	mu.Lock()
	cmdAfter := serverCmd
	mu.Unlock()
	if cmdAfter != nil {
		t.Fatalf("a server process was started for a file with no server config")
	}
}

// TestTouchFilePythonProjectUsesPythonServer asserts that a Python file in a
// project without a Go module is never handed to gopls. When pyright is absent
// the start must fail with a pyright-specific error; when it is installed the
// diagnostics call must succeed. Either way no gopls process may be spawned.
func TestTouchFilePythonProjectUsesPythonServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping LSP integration test in short mode")
	}
	if os.Getenv("LSP_TEST") == "" {
		t.Skip("skipping: set LSP_TEST=1 to run LSP integration tests")
	}

	proj := t.TempDir()
	py := filepath.Join(proj, "app.py")
	if err := os.WriteFile(py, []byte("x: int = 1\n"), 0644); err != nil {
		t.Fatalf("write app.py: %v", err)
	}

	Init(proj)
	defer Init("")

	_, err := TouchFile(py, true)
	_, lookErr := exec.LookPath("pyright")
	if lookErr == nil {
		if err != nil {
			t.Fatalf("TouchFile(app.py) with pyright installed failed: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("TouchFile(app.py) succeeded without pyright on PATH")
	}
	if !strings.Contains(err.Error(), "pyright") {
		t.Fatalf("error = %v, want it to name the pyright server", err)
	}

	mu.Lock()
	cmd := serverCmd
	mu.Unlock()
	if cmd != nil {
		t.Fatalf("server process %q was left running after a failed start", cmd.Path)
	}
}
