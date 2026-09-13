package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

func TestForkSession(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	// Create a parent session
	parent := New("test-main", dir)
	for i := 0; i < 5; i++ {
		parent.Append(types.Message{Role: "user", Content: "msg"})
	}
	parent.Flush()

	// Fork at message 3
	branch, err := store.Fork("test-main", 3, "try-pg")
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}
	if branch.ID != "test-main-try-pg" {
		t.Errorf("want branch ID 'test-main-try-pg', got %q", branch.ID)
	}
	if branch.ParentSessionID != "test-main" {
		t.Errorf("want parent 'test-main', got %q", branch.ParentSessionID)
	}
	if branch.ForkAt != 3 {
		t.Errorf("want fork_at=3, got %d", branch.ForkAt)
	}
	if len(branch.Messages) != 3 {
		t.Errorf("want 3 shared messages, got %d", len(branch.Messages))
	}
}

// TestForkClampsForkAt guards against the slice-bounds panic that occurred
// when the TUI passed its in-memory message count as forkAt.
func TestForkClampsForkAt(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	parent := New("clamp-main", dir)
	parent.Append(types.Message{Role: "user", Content: "one"})
	parent.Append(types.Message{Role: "assistant", Content: "two"})
	parent.Flush()

	// forkAt far beyond the persisted message count must not panic
	branch, err := store.Fork("clamp-main", 7, "big")
	if err != nil {
		t.Fatalf("Fork with oversized forkAt failed: %v", err)
	}
	if len(branch.Messages) != 2 {
		t.Errorf("want 2 clamped messages, got %d", len(branch.Messages))
	}
	if branch.ForkAt != 2 {
		t.Errorf("want fork_at clamped to 2, got %d", branch.ForkAt)
	}

	// negative forkAt must not panic either
	if _, err := store.Fork("clamp-main", -3, "neg"); err != nil {
		t.Fatalf("Fork with negative forkAt failed: %v", err)
	}
}

func TestForkWithAutoLabel(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	parent := New("test-main", dir)
	for i := 0; i < 3; i++ {
		parent.Append(types.Message{Role: "user", Content: "msg"})
	}
	parent.Flush()

	// Fork without label — should auto-generate
	branch, err := store.Fork("test-main", 2, "")
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}
	if branch.ID != "test-main-branch-1" {
		t.Errorf("want auto ID 'test-main-branch-1', got %q", branch.ID)
	}

	// Second fork without label — should get -2
	branch2, err := store.Fork("test-main", 2, "")
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}
	if branch2.ID != "test-main-branch-2" {
		t.Errorf("want auto ID 'test-main-branch-2', got %q", branch2.ID)
	}
}

func TestForkInvalidParent(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	_, err := store.Fork("nonexistent", 0, "")
	if err == nil {
		t.Fatal("expected error for nonexistent parent")
	}
}

func TestForkPersistsToDisk(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)

	parent := New("test-main", dir)
	parent.Append(types.Message{Role: "user", Content: "Hello"})
	parent.Append(types.Message{Role: "assistant", Content: "Hi"})
	parent.Flush()

	branch, err := store.Fork("test-main", 1, "branch-a")
	if err != nil {
		t.Fatalf("Fork failed: %v", err)
	}

	// Add new messages and flush
	branch.Append(types.Message{Role: "user", Content: "New branch msg"})
	branch.Flush()

	// Verify the file exists
	path := filepath.Join(dir, "test-main-branch-a.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("branch file was not created on disk")
	}

	// Reload and verify
	loaded, err := store.Load("test-main-branch-a")
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if len(loaded.Messages) != 2 {
		t.Errorf("want 2 messages (1 shared + 1 new), got %d", len(loaded.Messages))
	}
	if loaded.Messages[1].Content != "New branch msg" {
		t.Errorf("want new message content, got %q", loaded.Messages[1].Content)
	}
}

// TestValidateID covers the charset that guards session file names.
func TestValidateID(t *testing.T) {
	valid := []string{"a", "TUI-20260913-120000", "branch_1", "v1.2.3", "A1"}
	for _, id := range valid {
		if err := ValidateID(id); err != nil {
			t.Errorf("ValidateID(%q) = %v, want nil", id, err)
		}
	}
	invalid := []string{"", ".", "..", "../x", "a/b", "/etc/passwd", `a\b`, "-leading", "with space", ".hidden"}
	for _, id := range invalid {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) = nil, want error", id)
		}
	}
}

// TestForkLabelTraversalRejected guards against /fork <label> escaping the
// session directory (the label becomes part of the branch file name).
func TestForkLabelTraversalRejected(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "sessions")
	store := NewStore(dir)

	parent := store.Create("parent")
	parent.Append(types.Message{Role: "user", Content: "hi"})
	if err := parent.Flush(); err != nil {
		t.Fatal(err)
	}

	for _, label := range []string{"../../../../pwned", "..", "a/b", "/tmp/evil"} {
		if _, err := store.Fork("parent", 1, label); err == nil {
			t.Errorf("Fork with label %q succeeded, want error", label)
		}
	}
	if _, err := os.Stat(filepath.Join(base, "pwned.json")); err == nil {
		t.Error("a fork label escaped the session directory")
	}
	// Nothing may be created next to the session directory either.
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "sessions" {
			t.Errorf("unexpected file created outside the session dir: %s", e.Name())
		}
	}
}

// TestSessionIDTraversalRejected guards Load/Delete against ids that escape the
// session directory (they come from CLI flags and user input).
func TestSessionIDTraversalRejected(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "sessions")
	store := NewStore(dir)

	victim := filepath.Join(base, "victim.json")
	if err := os.WriteFile(victim, []byte(`{"id":"victim","messages":[]}`), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Load("../victim"); err == nil {
		t.Error("Load with a traversal id succeeded, want error")
	}
	if err := store.Delete("../victim"); err == nil {
		t.Error("Delete with a traversal id succeeded, want error")
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("file outside the session dir was removed: %v", err)
	}

	// Valid ids must still work.
	ok := store.Create("valid-id")
	ok.Append(types.Message{Role: "user", Content: "x"})
	if err := ok.Flush(); err != nil {
		t.Fatalf("flush valid session: %v", err)
	}
	if _, err := store.Load("valid-id"); err != nil {
		t.Errorf("load valid session: %v", err)
	}
	if err := store.Delete("valid-id"); err != nil {
		t.Errorf("delete valid session: %v", err)
	}
}
