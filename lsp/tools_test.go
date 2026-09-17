package lsp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// installMockClient swaps the package-level persistent session for a
// mock-backed client and restores the previous state when the test ends, so
// tests never touch a real language server.
func installMockClient(t *testing.T, m *mockLSP, c *Conn, root string) *Client {
	t.Helper()
	cl := NewClient(c)
	if err := cl.Initialize("file://" + root); err != nil {
		t.Fatalf("initialize mock client: %v", err)
	}

	mu.Lock()
	prevClient, prevConn, prevRoot, prevAvail := client, conn, projectRoot, lspAvailable
	client, conn, projectRoot, lspAvailable = cl, c, root, true
	mu.Unlock()

	t.Cleanup(func() {
		mu.Lock()
		client, conn, projectRoot, lspAvailable = prevClient, prevConn, prevRoot, prevAvail
		mu.Unlock()
		m.close()
	})
	return cl
}

// TestExecuteViaPersistentResults covers the formatting of every navigation and
// query result on the persistent connection.
func TestExecuteViaPersistentResults(t *testing.T) {
	root := t.TempDir()
	m, c := newMockLSP()
	installMockClient(t, m, c, root)
	uri := "file://" + filepath.Join(root, "main.go")

	tests := []struct {
		name string
		tt   ToolType
		want string
	}{
		{"definition", ToolGoToDefinition, "Definition at file:///demo/main.go:5:6"},
		{"references", ToolFindReferences, "Found 2 references:\n  file:///demo/main.go:2:3\n  file:///demo/util.go:10:1\n"},
		{"hover", ToolHover, "func main()"},
		{"symbols", ToolDocumentSymbols, "Symbols in " + uri + ":\n  main (file:///demo/main.go:5)\n"},
	}
	for _, tc := range tests {
		got, err := executeViaPersistent(context.Background(), tc.tt, uri, 4, 5)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestExecuteViaPersistentEmptyResults covers the "server answered null" paths.
func TestExecuteViaPersistentEmptyResults(t *testing.T) {
	root := t.TempDir()
	m, c := newMockLSP()
	m.setNullResults(true)
	installMockClient(t, m, c, root)
	uri := "file://" + filepath.Join(root, "main.go")

	tests := []struct {
		name string
		tt   ToolType
		want string
	}{
		{"definition", ToolGoToDefinition, "No definition found."},
		{"references", ToolFindReferences, "No references found."},
		{"hover", ToolHover, "No hover information available."},
		{"symbols", ToolDocumentSymbols, "No symbols found."},
	}
	for _, tc := range tests {
		got, err := executeViaPersistent(context.Background(), tc.tt, uri, 0, 0)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestExecuteViaPersistentWithoutClient verifies the guard used when the LSP
// tool is invoked before a server ever started.
func TestExecuteViaPersistentWithoutClient(t *testing.T) {
	mu.Lock()
	prev := client
	client = nil
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		client = prev
		mu.Unlock()
	})

	if _, err := executeViaPersistent(context.Background(), ToolHover, "file:///x", 0, 0); err == nil {
		t.Fatal("expected an error when no client is installed")
	}
}

// TestExecuteViaPersistentUnknownTool verifies the unknown-tool error branch.
func TestExecuteViaPersistentUnknownTool(t *testing.T) {
	root := t.TempDir()
	m, c := newMockLSP()
	installMockClient(t, m, c, root)

	if _, err := executeViaPersistent(context.Background(), ToolType("bogus"), "file:///x", 0, 0); err == nil {
		t.Fatal("expected an error for an unknown tool type")
	}
}

// TestSnapshotBaselineDelta verifies that diagnostics already present before an
// edit are not reported again, so the agent only sees what it broke.
func TestSnapshotBaselineDelta(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	m, c := newMockLSP()
	installMockClient(t, m, c, root)
	uri := "file://" + canonicalPath(path)

	oldDiag := Diagnostic{
		Severity: 1,
		Range:    Range{Start: Position{Line: 3, Character: 1}},
		Message:  "pre-existing error",
	}
	m.addDiag(uri, []Diagnostic{oldDiag})
	SnapshotBaseline(path)

	m.addDiag(uri, []Diagnostic{oldDiag, {
		Severity: 1,
		Range:    Range{Start: Position{Line: 7, Character: 2}},
		Message:  "new error",
	}})
	got := GetNewDiagnostics(path)
	if len(got) != 1 {
		t.Fatalf("GetNewDiagnostics returned %d diagnostics, want 1: %#v", len(got), got)
	}
	if got[0].Message != "new error" {
		t.Fatalf("GetNewDiagnostics = %#v, want only the new error", got)
	}

	// Once the new error is fixed, nothing is reported as new.
	m.addDiag(uri, []Diagnostic{oldDiag})
	if extra := GetNewDiagnostics(path); len(extra) != 0 {
		t.Fatalf("GetNewDiagnostics after the fix = %#v, want none", extra)
	}
}

// TestInitRebuildsSessionOnRootChange exercises shutdownLocked: a language
// server is bound to one workspace, so switching roots must tear it down and
// reset the session instead of reusing it.
func TestInitRebuildsSessionOnRootChange(t *testing.T) {
	rootA, rootB := t.TempDir(), t.TempDir()

	m, c := newMockLSP()
	installMockClient(t, m, c, rootA)

	// Same root: the running session is kept.
	Init(rootA)
	if !IsAvailable() {
		t.Fatal("session was torn down although the root did not change")
	}

	// Different root: the server must be gone and the state reset.
	Init(rootB)
	if IsAvailable() {
		t.Fatal("session survived a workspace change")
	}
	mu.Lock()
	gotClient, gotConn, gotCmd, gotRoot := client, conn, serverCmd, projectRoot
	mu.Unlock()
	if gotClient != nil || gotConn != nil || gotCmd != nil {
		t.Fatalf("session not cleared: client=%v conn=%v cmd=%v", gotClient != nil, gotConn != nil, gotCmd != nil)
	}
	if gotRoot != rootB {
		t.Fatalf("projectRoot = %q, want %q", gotRoot, rootB)
	}
}
