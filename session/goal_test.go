package session

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/goal"
	"github.com/yusiwen/tinycode/types"
)

func testGoal() *goal.Goal {
	return &goal.Goal{
		ID:            "goal-1",
		Objective:     "make the failing test pass",
		Phase:         goal.PhaseActive,
		RoundsStarted: 2,
		MaxGoalRounds: 8,
		Revision:      3,
		CreatedAt:     time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 10, 19, 12, 5, 0, 0, time.UTC),
	}
}

// TestGoalSurvivesTheSessionFile is the persistence half of the capability: the
// session file is the only durable log a resume reads, so a goal that is not in
// it does not survive a restart. Activation is deliberately absent — see
// TestSessionWithoutAGoalWritesNoActivation and goal.Service.
func TestGoalSurvivesTheSessionFile(t *testing.T) {
	dir := t.TempDir()
	s := New("goal-session", dir)
	s.Goal = testGoal()
	if err := s.Append(types.Message{Role: types.RoleUser, Content: "work"}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	loaded, err := Load("goal-session", dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(loaded.Goal, s.Goal) {
		t.Errorf("the goal changed through the file:\n got %+v\nwant %+v", loaded.Goal, s.Goal)
	}
	if err := loaded.Goal.Validate(); err != nil {
		t.Errorf("the persisted goal does not validate: %v", err)
	}
}

// TestForkCarriesTheGoal pins the fork semantics: a branch is a different
// attempt at the same objective, so the phase and the rounds already spent come
// with it. Whether it may continue on its own does not, because activation is
// never written to a file.
func TestForkCarriesTheGoal(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	parent := New("fork-goal", dir)
	parent.Goal = testGoal()
	parent.Append(types.Message{Role: "user", Content: "one"})
	parent.Append(types.Message{Role: "assistant", Content: "two"})
	if err := parent.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	branch, err := store.Fork("fork-goal", 1, "attempt-2")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if branch.Goal == nil {
		t.Fatal("the branch lost the goal")
	}
	if !reflect.DeepEqual(branch.Goal, parent.Goal) {
		t.Errorf("the branch goal = %+v, want %+v", branch.Goal, parent.Goal)
	}
	if branch.Goal == parent.Goal {
		t.Error("the branch shares the parent's goal pointer; a mutation would cross branches")
	}
	branch.Goal.RoundsStarted = 7
	if parent.Goal.RoundsStarted != 2 {
		t.Errorf("mutating the branch changed the parent: rounds = %d", parent.Goal.RoundsStarted)
	}
}

// TestSessionWithoutAGoalWritesNoGoalKey keeps old session files and
// goal-less sessions byte-clean: no empty object, no activation field.
func TestSessionWithoutAGoalWritesNoGoalKey(t *testing.T) {
	dir := t.TempDir()
	s := New("no-goal", dir)
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	path, err := pathFor(dir, "no-goal")
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, key := range []string{"\"goal\"", "\"armed\""} {
		if strings.Contains(string(data), key) {
			t.Errorf("a session without a goal carries %s: %s", key, data)
		}
	}
}

// TestHandWrittenGoalLoadsAndValidates covers the case a user edits a session
// file: Load stays permissive (the transcript is still worth reading), and the
// goal is validated when it is restored, which is what refuses nonsense.
func TestHandWrittenGoalLoadsAndValidates(t *testing.T) {
	dir := t.TempDir()
	s := New("hand-written", dir)
	s.Goal = testGoal()
	s.Goal.RoundsStarted = 99 // beyond the cap
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	loaded, err := Load("hand-written", dir)
	if err != nil {
		t.Fatalf("load must not reject a bad goal: %v", err)
	}
	if err := loaded.Goal.Validate(); err == nil {
		t.Fatal("the invalid goal validated")
	}
}
