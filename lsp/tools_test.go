package lsp

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
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
	SnapshotBaseline(path, sourceFor(t, path))

	m.addDiag(uri, []Diagnostic{oldDiag, {
		Severity: 1,
		Range:    Range{Start: Position{Line: 7, Character: 2}},
		Message:  "new error",
	}})
	got := GetNewDiagnostics(path, sourceFor(t, path))
	if len(got) != 1 {
		t.Fatalf("GetNewDiagnostics returned %d diagnostics, want 1: %#v", len(got), got)
	}
	if got[0].Message != "new error" {
		t.Fatalf("GetNewDiagnostics = %#v, want only the new error", got)
	}

	// Once the new error is fixed, nothing is reported as new.
	m.addDiag(uri, []Diagnostic{oldDiag})
	if extra := GetNewDiagnostics(path, sourceFor(t, path)); len(extra) != 0 {
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

// TestSyncFileSendsTheCallerContent pins the contract issue #7 S2 introduced:
// the bytes the caller passes are what the language server sees, not what is on
// disk. A regression that opened the path again would send the fixture text, and
// the sandbox decision that covered the caller's read would no longer cover this
// one.
func TestSyncFileSendsTheCallerContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc onDisk() {}\n"), 0644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}

	m, c := newMockLSP()
	installMockClient(t, m, c, root)

	uri := "file://" + canonicalPath(path)
	// The mock answers the diagnostics request only after it has recorded the
	// text, so waiting for them makes the assertion below deterministic instead of
	// racing the reader goroutine.
	m.addDiag(uri, nil)

	const fromCaller = "package main\n\nfunc fromCaller() {}\n"
	if _, err := SyncFile(path, fromCaller, true); err != nil {
		t.Fatalf("SyncFile: %v", err)
	}

	if got := m.textFor(uri); got != fromCaller {
		t.Errorf("server was told %q, want the caller's content %q", got, fromCaller)
	}
}

// requireRealServer skips the integration tests that need a language server
// binary, the same way lsp_test.go does.
func requireRealServer(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping LSP integration test in short mode")
	}
	if os.Getenv("LSP_TEST") == "" {
		t.Skip("skipping: set LSP_TEST=1 to run LSP integration tests")
	}
}

// restoreSession snapshots the package-level session and restores it when the
// test ends, stopping whatever the test started: these tests spawn a real server
// as a child process.
func restoreSession(t *testing.T) {
	t.Helper()
	mu.Lock()
	prevClient, prevConn, prevCmd := client, conn, serverCmd
	prevRoot, prevAvail := projectRoot, lspAvailable
	mu.Unlock()
	t.Cleanup(func() {
		mu.Lock()
		shutdownLocked()
		client, conn, serverCmd = prevClient, prevConn, prevCmd
		projectRoot, lspAvailable = prevRoot, prevAvail
		mu.Unlock()
	})
}

// TestToolCallPromotesTheSessionServer covers issue #114: with a workspace
// configured, a cold LSP tool call must start the session's server and use it.
// Before the fix it started a server of its own that the session never learned
// about, so IsAvailable() stayed false — /diagnostics then reported "LSP not
// available (set lsp.enabled=true in config.json)", which is wrong advice with
// LSP on — and a second call paid another start.
func TestToolCallPromotesTheSessionServer(t *testing.T) {
	requireRealServer(t)
	proj := setupDemoProject(t)
	restoreSession(t)
	Init(proj)

	var serverLog bytes.Buffer
	prevWriter := log.Writer()
	log.SetOutput(&serverLog)
	t.Cleanup(func() { log.SetOutput(prevWriter) })

	tool := ToolFactory(ToolDocumentSymbols)
	file := filepath.Join(proj, "main.go")
	for call := 1; call <= 2; call++ {
		got, err := tool.Execute(context.Background(), map[string]any{"file_path": file})
		if err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
		if !strings.Contains(got, "main") {
			t.Fatalf("call %d returned %q, want the symbols of main.go", call, got)
		}
	}

	if !IsAvailable() {
		t.Error("a tool call left the session without a client; /diagnostics would report LSP as unavailable")
	}
	if starts := strings.Count(serverLog.String(), "started for"); starts != 1 {
		t.Errorf("server started %d times across two calls, want 1:\n%s", starts, serverLog.String())
	}
}

// TestToolCallWithoutWorkspaceServesTheRequest covers the shipped default: LSP is
// not enabled, so Init was never called and the tool has to start a server for
// the call alone. That is the only route with no session to promote, and it
// answered "LSP error 0: no views" until issue #114 rooted the per-call server at
// the project directory instead of at the file.
func TestToolCallWithoutWorkspaceServesTheRequest(t *testing.T) {
	requireRealServer(t)
	proj := setupDemoProject(t)
	restoreSession(t)
	mu.Lock()
	client, conn, serverCmd = nil, nil, nil
	projectRoot, lspAvailable = "", false
	mu.Unlock()

	got, err := ToolFactory(ToolDocumentSymbols).Execute(context.Background(), map[string]any{
		"file_path": filepath.Join(proj, "main.go"),
	})
	if err != nil {
		t.Fatalf("tool call: %v", err)
	}
	if !strings.Contains(got, "main") {
		t.Errorf("result = %q, want the symbols of main.go", got)
	}
	if IsAvailable() {
		t.Error("a per-call server must not become the session's client")
	}
}
