package session

import (
	"os"
	"path/filepath"
	"strings"
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

// TestFlushIsAtomicAndPrivate checks that Flush writes the whole file through a
// temp file (no leftovers), keeps mode 0600 and replaces previous content.
func TestFlushIsAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	s := New("atomic", dir)
	s.Append(types.Message{Role: "user", Content: "first"})
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	path := filepath.Join(dir, "atomic.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("session file mode = %o, want 600", perm)
	}

	// Replacing the content must not append or corrupt the file.
	s.Messages = nil
	s.Append(types.Message{Role: "user", Content: "second"})
	if err := s.Flush(); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	loaded, err := Load("atomic", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Messages) != 1 || loaded.Messages[0].Content != "second" {
		t.Errorf("reloaded messages = %+v, want a single 'second'", loaded.Messages)
	}

	// No temp files may be left behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

// TestExportMarkdownDoesNotRaceAppend runs export concurrently with appends;
// the race detector fails this without the session mutex.
func TestExportMarkdownDoesNotRaceAppend(t *testing.T) {
	dir := t.TempDir()
	s := New("export-race", dir)
	s.Append(types.Message{Role: "user", Content: "hello"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			s.Append(types.Message{Role: "assistant", Content: "reply"})
		}
	}()
	for i := 0; i < 50; i++ {
		if md := s.ExportMarkdown(); !strings.Contains(md, "# Session:") {
			t.Errorf("export lost its header")
			break
		}
	}
	<-done
}

// TestForkRejectsDuplicateLabel guards against silently overwriting an existing
// branch when the same label is used twice.
func TestForkRejectsDuplicateLabel(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	parent := store.Create("dup-parent")
	parent.Append(types.Message{Role: "user", Content: "hi"})
	if err := parent.Flush(); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Fork("dup-parent", 1, "same"); err != nil {
		t.Fatalf("first fork: %v", err)
	}
	if _, err := store.Fork("dup-parent", 1, "same"); err == nil {
		t.Error("second fork with the same label succeeded, want an error")
	}
}
