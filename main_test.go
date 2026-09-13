package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadProjectContextNoFile(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	ctx := loadProjectContext()
	if ctx != "" {
		t.Errorf("expected empty for no file, got %q", ctx)
	}
}

func TestLoadProjectContextAGENTSMD(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	content := "This project uses Go 1.24 and follows standard Go conventions."
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0644)

	ctx := loadProjectContext()
	if ctx != content {
		t.Errorf("expected AGENTS.md content, got %q", ctx)
	}
}

func TestLoadProjectContextCLAUDEMD(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	content := "Always run tests before committing."
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(content), 0644)

	ctx := loadProjectContext()
	if ctx != content {
		t.Errorf("expected CLAUDE.md content, got %q", ctx)
	}
}

func TestLoadProjectContextPrecedence(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	// Both files exist — AGENTS.md takes precedence
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agents content"), 0644)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude content"), 0644)

	ctx := loadProjectContext()
	if ctx != "agents content" {
		t.Errorf("expected AGENTS.md content (first match), got %q", ctx)
	}
}

// TestInfoCommandsDoNotTouchSessions is the regression test for the data-loss
// bug where --list-sessions (and the other informational commands) created and
// flushed an empty "default" session, overwriting a saved conversation.
func TestInfoCommandsDoNotTouchSessions(t *testing.T) {
	// Isolate the user config and log dir from the developer's real HOME.
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	dir := t.TempDir()
	saved := `{"id":"default","title":"precious","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","message_count":1,"messages":[{"role":"user","content":"do not lose me"}]}`
	path := filepath.Join(dir, "default.json")
	if err := os.WriteFile(path, []byte(saved), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--list-sessions", "--session-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--list-sessions failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("saved session disappeared: %v", err)
	}
	if !strings.Contains(string(data), "precious") || !strings.Contains(string(data), "do not lose me") {
		t.Errorf("saved session was overwritten: %s", data)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("informational command created files: %v", names)
	}
}

// TestExpandPath covers the "~" and "$VAR" expansion used for configured paths.
func TestExpandPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TINYCODE_TEST_DIR", "/tmp/tinycode-expand")

	cases := []struct{ in, want string }{
		{"~/.tinycode/sessions", filepath.Join(home, ".tinycode/sessions")},
		{"~", home},
		{"$TINYCODE_TEST_DIR/logs", "/tmp/tinycode-expand/logs"},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"~notahome/x", "~notahome/x"},
	}
	for _, tc := range cases {
		if got := expandPath(tc.in); got != tc.want {
			t.Errorf("expandPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestExportSessionWritesPrivateMarkdown covers --export-session end to end:
// the transcript is written next to the session file's name and is not
// world-readable.
func TestExportSessionWritesPrivateMarkdown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	t.Chdir(work)

	dir := t.TempDir()
	sess := `{"id":"exp","title":"exported","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","message_count":2,"messages":[{"role":"user","content":"hello there"},{"role":"assistant","content":"hi"}]}`
	if err := os.WriteFile(filepath.Join(dir, "exp.json"), []byte(sess), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--export-session", "exp", "--session-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--export-session failed: %v", err)
	}

	out := filepath.Join(work, "exp.md")
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("export missing: %v", err)
	}
	if !strings.Contains(string(data), "hello there") || !strings.Contains(string(data), "hi") {
		t.Errorf("export does not contain the conversation:\n%s", data)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("export mode = %o, want 600", perm)
	}
}

// TestDeleteSessionCommand covers the --delete-session path (and that a
// traversal id is refused rather than deleting something else).
func TestDeleteSessionCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gone.json"), []byte(`{"id":"gone"}`), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--delete-session", "gone", "--session-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--delete-session failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.json")); err == nil {
		t.Error("session was not deleted")
	}

	// A traversal id must be rejected by the store, not acted on.
	cmd = newRootCmd()
	cmd.SetArgs([]string{"--delete-session", "../escape", "--session-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Error("expected a traversal session id to be rejected")
	}
}

// TestInvalidAgentPermissionEffectIsRejected checks that a bad effect in a
// configured ruleset fails loudly instead of being silently ignored.
func TestInvalidAgentPermissionEffectIsRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	if err := os.MkdirAll(".tinycode", 0755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"agents": {"plan": {"permissions": [{"action": "bash", "effect": "banana"}]}}}`
	if err := os.WriteFile(filepath.Join(".tinycode", "config.json"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--list-sessions", "--session-dir", t.TempDir()})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for an unknown permission effect")
	}
	if !strings.Contains(err.Error(), "banana") {
		t.Errorf("error should name the bad effect, got: %v", err)
	}
}
