package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/session"
	"github.com/yusiwen/tinycode/types"
)

// branchStore writes a parent and a branch into a temp store: the two shapes
// /session and /compare are read on.
func branchStore(t *testing.T) *session.Store {
	t.Helper()
	store := session.NewStore(t.TempDir())
	main := session.New("TUI-20260101-000000", store.Dir)
	main.CreatedAt = time.Unix(100, 0)
	for _, c := range []string{"one", "two", "three"} {
		main.Messages = append(main.Messages, types.Message{Role: "user", Content: c})
	}
	if err := main.Flush(); err != nil {
		t.Fatalf("flush parent: %v", err)
	}
	branch, err := store.Fork("TUI-20260101-000000", 2, "try")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if err := branch.Flush(); err != nil {
		t.Fatalf("flush branch: %v", err)
	}
	return store
}

func TestSessionTreeTextHangsTheBranchUnderItsParent(t *testing.T) {
	store := branchStore(t)

	got := sessionTreeText(store, "TUI-20260101-000000-try")
	if !strings.Contains(got, "└─ TUI-20260101-000000-try") {
		t.Fatalf("branch is not drawn under its parent:\n%s", got)
	}
	if !strings.Contains(got, "* └─ TUI-20260101-000000-try") {
		t.Errorf("the current branch is not marked:\n%s", got)
	}
	if !strings.Contains(got, ", forked at 2") {
		t.Errorf("the fork point is missing:\n%s", got)
	}
	// The parent is a root: two leading spaces (the marker column) and no guide.
	if !strings.Contains(got, "\n  TUI-20260101-000000 ") {
		t.Errorf("the parent is not drawn as a root:\n%s", got)
	}
}

func TestSessionTreeTextOnAnEmptyStore(t *testing.T) {
	if got := sessionTreeText(session.NewStore(t.TempDir()), ""); got != "No saved sessions." {
		t.Errorf("empty store rendered %q", got)
	}
}

func TestFormatComparisonSeparatesLinkFromTranscript(t *testing.T) {
	store := branchStore(t)
	cmp, err := store.Compare("TUI-20260101-000000", "TUI-20260101-000000-try")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}

	got := formatComparison(cmp)
	for _, want := range []string{
		"common ancestor  TUI-20260101-000000: a is an ancestor of b",
		"fork             TUI-20260101-000000-try forked at message 2",
		"common prefix    2 messages identical",
		`a then           user: "three"`,
		"b ends at        message 2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("comparison is missing %q:\n%s", want, got)
		}
	}
}

func TestFormatComparisonReportsIdenticalTranscripts(t *testing.T) {
	store := branchStore(t)
	// The branch is the parent's two-message prefix, extended back to parity by
	// copying the third message: same link, same transcript.
	branch, err := store.Load("TUI-20260101-000000-try")
	if err != nil {
		t.Fatal(err)
	}
	main, err := store.Load("TUI-20260101-000000")
	if err != nil {
		t.Fatal(err)
	}
	branch.Messages = append(branch.Messages, main.Messages[2])
	if err := branch.Flush(); err != nil {
		t.Fatal(err)
	}

	cmp, err := store.Compare("TUI-20260101-000000", "TUI-20260101-000000-try")
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	got := formatComparison(cmp)
	if !strings.Contains(got, "transcripts      identical") {
		t.Errorf("identical transcripts not reported:\n%s", got)
	}
}

// TestBoxDrawnSystemMessageKeepsItsLines guards the render path the tree needs: a
// multi-line message containing a box-drawing guide goes through the no-wrap
// branch (wrapping would break a table's borders), and that branch used to append
// the whole chunk, so its newlines became cells and the tree flowed onto one row.
func TestBoxDrawnSystemMessageKeepsItsLines(t *testing.T) {
	text := "Sessions and branches:\n  main  (1 msgs)\n  └─ main-try  (2 msgs, forked at 1)"
	m := frameModel(80, 24)
	m.messages = []chatMessage{{Role: "system", Content: text}}

	view := stripANSIView(m.View())
	// The grid is the assertion, not the text: a chunk appended whole keeps its
	// newlines as cells, so the same three lines come back out of a *single* row
	// whose cells carry line feeds — the terminal then wraps it past its width.
	if got := m.grid.RowCount(); got != 3 {
		t.Fatalf("a three-line box-drawn message occupied %d grid row(s), want 3:\n%s", got, view)
	}
	if !strings.Contains(view, "└─ main-try  (2 msgs, forked at 1)") {
		t.Errorf("the branch line is missing:\n%s", view)
	}
}
