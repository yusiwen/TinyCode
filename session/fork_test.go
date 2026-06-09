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
