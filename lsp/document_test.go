package lsp

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestLanguageIDForPath pins the LSP language ids (which differ from the
// language names used to pick a server).
func TestLanguageIDForPath(t *testing.T) {
	cases := map[string]string{
		"/p/main.go":      "go",
		"/p/app.py":       "python",
		"/p/types.pyi":    "python",
		"/p/index.ts":     "typescript",
		"/p/App.tsx":      "typescriptreact",
		"/p/app.js":       "javascript",
		"/p/App.jsx":      "javascriptreact",
		"/p/lib.rs":       "rust",
		"/p/Main.java":    "java",
		"/p/main.c":       "c",
		"/p/main.cpp":     "cpp",
		"/p/header.hpp":   "cpp",
		"/p/notes.md":     "plaintext",
		"/p/no-extension": "plaintext",
		"/p/UPPER.GO":     "go",
	}
	for path, want := range cases {
		if got := languageIDForPath(path); got != want {
			t.Errorf("languageIDForPath(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestSyncDocumentOpensThenChanges covers the protocol sequence that fixes the
// empty-buffer bug: the first sync opens the document with its real text and the
// right language id, the second sends a full-text change with a higher version
// instead of a duplicate didOpen (which servers ignore).
func TestSyncDocumentOpensThenChanges(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	defer func() {
		_ = clientWrite.Close()
		_ = serverWrite.Close()
	}()

	conn := NewConn(clientWrite, clientRead)
	client := NewClient(conn)
	br := bufio.NewReader(serverRead)

	const uri = "file:///tmp/project/main.go"
	first := "package main\n"

	// The pipes are unbuffered, so the write must run while the test reads.
	writes := make(chan error, 2)
	go func() { writes <- client.SyncDocument(uri, first) }()

	var open struct {
		Method string `json:"method"`
		Params struct {
			TextDocument struct {
				URI        string `json:"uri"`
				LanguageID string `json:"languageId"`
				Version    int    `json:"version"`
				Text       string `json:"text"`
			} `json:"textDocument"`
		} `json:"params"`
	}
	if err := json.Unmarshal(readFrame(t, br), &open); err != nil {
		t.Fatalf("parse didOpen: %v", err)
	}
	if open.Method != "textDocument/didOpen" {
		t.Errorf("first notification = %q, want textDocument/didOpen", open.Method)
	}
	if open.Params.TextDocument.URI != uri {
		t.Errorf("uri = %q, want %q", open.Params.TextDocument.URI, uri)
	}
	if open.Params.TextDocument.LanguageID != "go" {
		t.Errorf("languageId = %q, want go", open.Params.TextDocument.LanguageID)
	}
	if open.Params.TextDocument.Text != first {
		t.Errorf("didOpen text = %q, want the file content %q", open.Params.TextDocument.Text, first)
	}
	if open.Params.TextDocument.Version != 1 {
		t.Errorf("first version = %d, want 1", open.Params.TextDocument.Version)
	}

	if err := <-writes; err != nil {
		t.Fatalf("first SyncDocument: %v", err)
	}

	second := "package main\n\nfunc main() {}\n"
	go func() { writes <- client.SyncDocument(uri, second) }()

	var change struct {
		Method string `json:"method"`
		Params struct {
			TextDocument struct {
				URI     string `json:"uri"`
				Version int    `json:"version"`
			} `json:"textDocument"`
			ContentChanges []struct {
				Text string `json:"text"`
			} `json:"contentChanges"`
		} `json:"params"`
	}
	if err := json.Unmarshal(readFrame(t, br), &change); err != nil {
		t.Fatalf("parse didChange: %v", err)
	}
	if change.Method != "textDocument/didChange" {
		t.Errorf("second notification = %q, want textDocument/didChange", change.Method)
	}
	if change.Params.TextDocument.Version != 2 {
		t.Errorf("second version = %d, want 2", change.Params.TextDocument.Version)
	}
	if len(change.Params.ContentChanges) != 1 || change.Params.ContentChanges[0].Text != second {
		t.Errorf("didChange payload = %+v, want the full new text %q", change.Params.ContentChanges, second)
	}
	if err := <-writes; err != nil {
		t.Fatalf("second SyncDocument: %v", err)
	}
}

// TestCanonicalPathResolvesSymlinks mirrors what a language server does to the
// workspace root: the URI we send must be the resolved path, otherwise the two
// never match (macOS /tmp is a symlink to /private/tmp).
func TestCanonicalPathResolvesSymlinks(t *testing.T) {
	base := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(base); err == nil {
		base = resolved
	}
	dir := filepath.Join(base, "proj")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "new.go") // not created yet

	got := canonicalPath(file)
	want := filepath.Join(dir, "new.go")
	if got != want {
		t.Errorf("canonicalPath(%q) = %q, want %q", file, got, want)
	}

	// A symlinked directory must be reported by its target.
	link := filepath.Join(base, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got := canonicalPath(filepath.Join(link, "new.go")); got != want {
		t.Errorf("canonicalPath through a symlink = %q, want %q", got, want)
	}
}
