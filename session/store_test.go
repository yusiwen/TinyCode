package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// storeFixture writes two sessions into a fresh store directory, plus the files
// a real session directory can contain but that must never be listed.
func storeFixture(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	st := NewStore(dir)

	a := st.Create("TUI-20260101-000000")
	_ = a.Append(types.Message{Role: types.RoleUser, Content: "Fix the parser"})
	_ = a.Append(types.Message{Role: types.RoleAssistant, Content: "refactored the tokenizer and parser"})
	if err := a.Flush(); err != nil {
		t.Fatalf("flush first session: %v", err)
	}

	b := st.Create("TUI-20260102-000000")
	_ = b.Append(types.Message{Role: types.RoleUser, Content: "Add tests"})
	_ = b.Append(types.Message{
		Role:             types.RoleAssistant,
		Content:          "Added store tests",
		ReasoningContent: "cover the store search path",
	})
	if err := b.Flush(); err != nil {
		t.Fatalf("flush second session: %v", err)
	}

	// Files a session directory may contain that are not sessions.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	return st, dir
}

// TestStoreList covers the listing the CLI's --list-sessions uses: only session
// files are returned, they carry the derived title and preview, and they are
// ordered by update time.
func TestStoreList(t *testing.T) {
	st, _ := storeFixture(t)

	infos := st.List()
	if len(infos) != 2 {
		t.Fatalf("List returned %d sessions, want 2: %+v", len(infos), infos)
	}
	byID := map[string]SessionInfo{}
	for _, info := range infos {
		byID[info.ID] = info
	}
	a, ok := byID["TUI-20260101-000000"]
	if !ok {
		t.Fatalf("first session missing from %+v", infos)
	}
	if a.Title != "Fix the parser" {
		t.Errorf("title = %q, want the first user message", a.Title)
	}
	if a.Preview != "refactored the tokenizer and parser" {
		t.Errorf("preview = %q, want the last assistant message", a.Preview)
	}
	if a.MessageCount != 2 {
		t.Errorf("message count = %d, want 2", a.MessageCount)
	}
	if a.CreatedAt.IsZero() || a.UpdatedAt.IsZero() {
		t.Errorf("timestamps were not carried over: %+v", a)
	}

	for i := 1; i < len(infos); i++ {
		if infos[i].UpdatedAt.Before(infos[i-1].UpdatedAt) {
			t.Fatalf("List is not sorted by update time: %+v", infos)
		}
	}

	// A directory that does not exist lists nothing instead of failing.
	if got := NewStore(filepath.Join(t.TempDir(), "missing")).List(); got != nil {
		t.Fatalf("List on a missing directory = %+v, want nil", got)
	}
}

// TestStoreSearch covers the matching rules of --search-sessions: titles,
// previews, message content and reasoning are searched case-insensitively, and
// unrelated files are ignored.
func TestStoreSearch(t *testing.T) {
	st, _ := storeFixture(t)

	tests := []struct {
		query string
		want  []string
	}{
		{"parser", []string{"TUI-20260101-000000"}},         // title
		{"tokenizer", []string{"TUI-20260101-000000"}},      // preview
		{"FIX THE PARSER", []string{"TUI-20260101-000000"}}, // case-insensitive, message content
		{"store search", []string{"TUI-20260102-000000"}},   // reasoning content
		{"add tests", []string{"TUI-20260102-000000"}},
		{"tests", []string{"TUI-20260102-000000"}},
		{"nothing-matches-this", nil},
	}

	for _, tc := range tests {
		got := st.Search(tc.query)
		ids := make([]string, 0, len(got))
		for _, info := range got {
			ids = append(ids, info.ID)
		}
		if len(ids) != len(tc.want) {
			t.Errorf("Search(%q) = %v, want %v", tc.query, ids, tc.want)
			continue
		}
		for i, id := range tc.want {
			if ids[i] != id {
				t.Errorf("Search(%q)[%d] = %q, want %q", tc.query, i, ids[i], id)
			}
		}
	}

	// An empty query matches every session (it is a substring of everything).
	if got := st.Search(""); len(got) != 2 {
		t.Errorf("Search(\"\") returned %d sessions, want 2", len(got))
	}

	// A missing directory searches nothing instead of failing.
	if got := NewStore(filepath.Join(t.TempDir(), "missing")).Search("x"); got != nil {
		t.Fatalf("Search on a missing directory = %+v, want nil", got)
	}
}
