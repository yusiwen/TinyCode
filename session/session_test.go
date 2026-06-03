package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

func TestNew(t *testing.T) {
	dir := t.TempDir()
	s := New("test-session", dir)
	if s == nil {
		t.Fatal("New returned nil")
	}
	if s.ID != "test-session" {
		t.Fatalf("expected ID 'test-session', got %q", s.ID)
	}
}

func TestAppendAndFlush(t *testing.T) {
	dir := t.TempDir()
	s := New("append-flush", dir)

	msg := types.Message{Role: types.RoleUser, Content: "hello"}
	if err := s.Append(msg); err != nil {
		t.Fatalf("Append error: %v", err)
	}
	if len(s.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(s.Messages))
	}

	if err := s.Flush(); err != nil {
		t.Fatalf("Flush error: %v", err)
	}

	path := filepath.Join(dir, "append-flush.json")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("Flush did not create the session file on disk")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	s := New("load-test", dir)
	s.Append(types.Message{Role: types.RoleSystem, Content: "be helpful"})
	s.Append(types.Message{Role: types.RoleUser, Content: "hi"})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load("load-test", dir)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if loaded.ID != "load-test" {
		t.Fatalf("expected ID 'load-test', got %q", loaded.ID)
	}
	if len(loaded.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(loaded.Messages))
	}
	if loaded.Messages[0].Role != types.RoleSystem {
		t.Fatalf("expected role %q, got %q", types.RoleSystem, loaded.Messages[0].Role)
	}
}

func TestLoadNonexistent(t *testing.T) {
	dir := t.TempDir()
	_, err := Load("no-such-session", dir)
	if err == nil {
		t.Fatal("expected error when loading nonexistent session")
	}
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	st := NewStore(dir)

	s := st.Create("store-test")
	if s.ID != "store-test" {
		t.Fatalf("expected ID 'store-test', got %q", s.ID)
	}

	s.Append(types.Message{Role: types.RoleUser, Content: "stored"})
	s.Flush()

	loaded, err := st.Load("store-test")
	if err != nil {
		t.Fatalf("Store.Load error: %v", err)
	}
	if len(loaded.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(loaded.Messages))
	}
}
