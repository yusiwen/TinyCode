package session

import (
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// seed writes one session file directly. The tree is built from the files, so a
// test needs the same kind of store a run leaves behind — not an in-memory
// structure the code under test never sees.
func seed(t *testing.T, store *Store, id, parent string, forkAt int, created int64, contents ...string) *Session {
	t.Helper()
	s := New(id, store.Dir)
	s.ParentSessionID = parent
	s.ForkAt = forkAt
	s.CreatedAt = time.Unix(created, 0)
	for _, c := range contents {
		s.Messages = append(s.Messages, types.Message{Role: "user", Content: c})
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("flush %s: %v", id, err)
	}
	return s
}

func renderTree(roots []*Node) []string {
	var out []string
	Walk(roots, func(n *Node) {
		label := n.ID
		if n.Cycle {
			label += " [cycle]"
		}
		if n.Orphan {
			label += " [orphan " + n.ParentSessionID + "]"
		}
		out = append(out, n.Indent+n.Connector+label)
	})
	return out
}

func TestTreeHangsBranchesUnderTheirParent(t *testing.T) {
	store := NewStore(t.TempDir())
	seed(t, store, "main", "", 0, 100, "one", "two")
	seed(t, store, "main-a", "main", 2, 200, "one", "two")
	seed(t, store, "main-a-b", "main-a", 1, 300, "one")
	seed(t, store, "main-c", "main", 1, 250, "one")

	got := renderTree(store.Tree())
	want := []string{
		"main",
		"├─ main-a",
		"│  └─ main-a-b",
		"└─ main-c",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tree =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestTreePlacesEverySessionExactlyOnce is the property the view depends on: a
// session that is in the store but not in the tree is a branch the user cannot
// see, which is the gap this work closes.
func TestTreePlacesEverySessionExactlyOnce(t *testing.T) {
	store := NewStore(t.TempDir())
	seed(t, store, "main", "", 0, 100, "one")
	seed(t, store, "main-a", "main", 1, 200, "one")
	seed(t, store, "main-a-b", "main-a", 1, 300, "one")
	seed(t, store, "main-c", "main", 1, 250, "one")
	seed(t, store, "other", "", 0, 400, "two")

	seen := map[string]int{}
	for _, line := range renderTree(store.Tree()) {
		id := line[strings.LastIndex(line, " ")+1:]
		seen[id]++
	}
	for _, id := range []string{"main", "main-a", "main-a-b", "main-c", "other"} {
		if seen[id] != 1 {
			t.Errorf("session %q appears %d times in the tree, want 1", id, seen[id])
		}
	}
	if len(seen) != 5 {
		t.Errorf("the tree names %d sessions, the store has 5: %v", len(seen), seen)
	}
}

func TestTreeMarksAMissingParentAsAnOrphan(t *testing.T) {
	store := NewStore(t.TempDir())
	seed(t, store, "kept", "deleted", 3, 100, "one", "two")

	roots := store.Tree()
	if len(roots) != 1 {
		t.Fatalf("want 1 root, got %d", len(roots))
	}
	n := roots[0]
	if !n.Orphan {
		t.Errorf("a session whose parent is not in the store must be marked orphan")
	}
	if n.ParentSessionID != "deleted" || n.Parent != "" {
		t.Errorf("orphan should keep the dangling id and resolve no parent, got parent=%q stored=%q", n.Parent, n.ParentSessionID)
	}
	if got := renderTree(roots)[0]; got != "kept [orphan deleted]" {
		t.Errorf("rendered %q, want the dangling parent named", got)
	}
}

// TestTreeBreaksACycleWithoutHidingASession covers a store edited by hand: a
// fork cannot create a cycle, but a file can be. The view must neither hang nor
// drop the component.
func TestTreeBreaksACycleWithoutHidingASession(t *testing.T) {
	store := NewStore(t.TempDir())
	seed(t, store, "cyc-a", "cyc-b", 1, 300, "one")
	seed(t, store, "cyc-b", "cyc-a", 1, 200, "one")
	seed(t, store, "cyc-c", "cyc-a", 1, 400, "one")

	done := make(chan []string, 1)
	go func() { done <- renderTree(store.Tree()) }()
	var got []string
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Tree did not return on a parent cycle")
	}

	// cyc-a has the smallest id, so it is the link that is dropped.
	want := []string{
		"cyc-a [cycle]",
		"├─ cyc-b",
		"└─ cyc-c",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("tree =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestCompareNamesTheCommonAncestorAndTheFork(t *testing.T) {
	store := NewStore(t.TempDir())
	seed(t, store, "main", "", 0, 100, "one", "two", "three")
	seed(t, store, "main-a", "main", 2, 200, "one", "two")

	cmp, err := store.Compare("main", "main-a")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if cmp.CommonAncestor != "main" || cmp.AncestorOf != "a" {
		t.Errorf("ancestry = %q/%q, want main and a", cmp.CommonAncestor, cmp.AncestorOf)
	}
	if len(cmp.Forks) != 1 || cmp.Forks[0].ID != "main-a" || cmp.Forks[0].At != 2 {
		t.Errorf("forks = %+v, want one fork of main-a at 2", cmp.Forks)
	}
	if cmp.CommonPrefix != 2 {
		t.Errorf("common prefix = %d, want 2", cmp.CommonPrefix)
	}
	if cmp.BFirstDiff != "" {
		t.Errorf("the branch has no message past the fork, got divergence %q", cmp.BFirstDiff)
	}
	if !strings.Contains(cmp.AFirstDiff, "three") {
		t.Errorf("a's next message = %q, want the one after the fork", cmp.AFirstDiff)
	}

	// The same relation read from the other side.
	cmp, err = store.Compare("main-a", "main")
	if err != nil {
		t.Fatalf("compare reversed: %v", err)
	}
	if cmp.CommonAncestor != "main" || cmp.AncestorOf != "b" {
		t.Errorf("reversed ancestry = %q/%q, want main and b", cmp.CommonAncestor, cmp.AncestorOf)
	}
}

func TestCompareReportsSiblingsUnderTheSharedAncestor(t *testing.T) {
	store := NewStore(t.TempDir())
	seed(t, store, "main", "", 0, 100, "one", "two")
	seed(t, store, "main-a", "main", 1, 200, "one")
	seed(t, store, "main-b", "main", 1, 300, "one")

	cmp, err := store.Compare("main-a", "main-b")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if cmp.CommonAncestor != "main" {
		t.Errorf("common ancestor = %q, want main", cmp.CommonAncestor)
	}
	if cmp.AncestorOf != "" {
		t.Errorf("siblings are not ancestors of each other, got %q", cmp.AncestorOf)
	}
	if len(cmp.Forks) != 2 || cmp.Forks[0].At != 1 || cmp.Forks[1].At != 1 {
		t.Errorf("forks = %+v, want both sides forked at 1", cmp.Forks)
	}
}

func TestCompareSeparatesTheLinkFromTheTranscript(t *testing.T) {
	store := NewStore(t.TempDir())
	seed(t, store, "main", "", 0, 100, "one", "two", "three")
	// The branch forked at 2, but its copied transcript was edited afterwards:
	// the link says two shared messages, the files agree on one.
	branch := seed(t, store, "main-a", "main", 2, 200, "one", "two")
	branch.Messages[1].Content = "edited after the fork"
	if err := branch.Flush(); err != nil {
		t.Fatalf("flush edited branch: %v", err)
	}

	cmp, err := store.Compare("main", "main-a")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if cmp.CommonPrefix != 1 {
		t.Errorf("common prefix = %d, want 1 — the transcripts disagree at index 1", cmp.CommonPrefix)
	}
	if cmp.Forks[0].At != 2 {
		t.Errorf("fork point = %d, want the recorded 2", cmp.Forks[0].At)
	}
	if !strings.Contains(cmp.BFirstDiff, "edited after the fork") {
		t.Errorf("b's first difference = %q", cmp.BFirstDiff)
	}
}

func TestCompareRefusesWhatItCannotCompare(t *testing.T) {
	store := NewStore(t.TempDir())
	seed(t, store, "main", "", 0, 100, "one")

	if _, err := store.Compare("main", "main"); err == nil {
		t.Error("comparing a session with itself must be refused")
	}
	if _, err := store.Compare("main", "missing"); err == nil {
		t.Error("comparing with a session that is not in the store must be refused")
	}
}

func TestCompareUnrelatedSessionsHaveNoAncestor(t *testing.T) {
	store := NewStore(t.TempDir())
	seed(t, store, "one", "", 0, 100, "alpha")
	seed(t, store, "two", "", 0, 200, "beta")

	cmp, err := store.Compare("one", "two")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if cmp.CommonAncestor != "" || len(cmp.Forks) != 0 {
		t.Errorf("unrelated sessions: ancestor=%q forks=%+v", cmp.CommonAncestor, cmp.Forks)
	}
	if cmp.CommonPrefix != 0 {
		t.Errorf("common prefix = %d, want 0", cmp.CommonPrefix)
	}
}
