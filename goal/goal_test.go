package goal

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func testGoal() *Goal {
	return &Goal{
		ID:            "goal-1",
		Objective:     "make the failing test pass",
		Phase:         PhaseActive,
		RoundsStarted: 2,
		MaxGoalRounds: 8,
		Revision:      3,
		CreatedAt:     time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 10, 19, 12, 5, 0, 0, time.UTC),
	}
}

func TestGoalValidate(t *testing.T) {
	blocked := testGoal()
	blocked.Phase = PhaseBlocked
	blocked.Blocker = &Blocker{Code: BlockerRoundLimit, Reason: "spent its rounds"}

	cases := []struct {
		name    string
		mutate  func(*Goal)
		wantErr bool
	}{
		{"active goal", func(*Goal) {}, false},
		{"blocked goal with a blocker", func(g *Goal) { *g = *blocked }, false},
		{"missing id", func(g *Goal) { g.ID = "" }, true},
		{"empty objective", func(g *Goal) { g.Objective = "  " }, true},
		{"unknown phase", func(g *Goal) { g.Phase = "running" }, true},
		{"zero round cap", func(g *Goal) { g.MaxGoalRounds = 0 }, true},
		{"negative rounds", func(g *Goal) { g.RoundsStarted = -1 }, true},
		{"rounds beyond the cap", func(g *Goal) { g.RoundsStarted = 9 }, true},
		{"missing revision", func(g *Goal) { g.Revision = 0 }, true},
		{"blocked without a blocker", func(g *Goal) { g.Phase = PhaseBlocked }, true},
		{"blocker on an active goal", func(g *Goal) { g.Blocker = &Blocker{Code: BlockerNoProgress} }, true},
		{"blocker without a code", func(g *Goal) {
			g.Phase = PhaseBlocked
			g.Blocker = &Blocker{Reason: "no code"}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := testGoal()
			tc.mutate(g)
			err := g.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}

	if err := (*Goal)(nil).Validate(); err == nil {
		t.Error("a nil goal must not validate")
	}
}

// TestGoalJSONRoundTrip pins the names on disk: a field renamed here breaks every
// session file written before it.
func TestGoalJSONRoundTrip(t *testing.T) {
	original := testGoal()
	original.Phase = PhaseBlocked
	original.Blocker = &Blocker{Code: BlockerModelReported, Reason: "needs a decision"}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"id", "objective", "phase", "rounds_started", "max_goal_rounds", "blocker", "revision", "created_at", "updated_at"} {
		if !containsKey(data, key) {
			t.Errorf("serialised goal is missing %s: %s", key, data)
		}
	}
	// Activation is process-local, so it must never appear on disk.
	if containsKey(data, "armed") {
		t.Errorf("serialised goal carries activation state: %s", data)
	}

	var loaded Goal
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(&loaded, original) {
		t.Errorf("round trip changed the goal:\n got %+v\nwant %+v", &loaded, original)
	}
}

func containsKey(data []byte, key string) bool {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return false
	}
	_, ok := raw[key]
	return ok
}

// TestCloneIsDeep guards the boundary between a reader's view and the live goal:
// a caller holding a Snapshot must not be able to mutate the service's state
// through the shared blocker pointer.
func TestCloneIsDeep(t *testing.T) {
	original := testGoal()
	original.Phase = PhaseBlocked
	original.Blocker = &Blocker{Code: BlockerRoundLimit, Reason: "original"}

	clone := original.Clone()
	clone.Objective = "changed"
	clone.Blocker.Reason = "changed"

	if original.Objective != "make the failing test pass" {
		t.Errorf("the clone's objective change reached the original: %q", original.Objective)
	}
	if original.Blocker.Reason != "original" {
		t.Errorf("the clone's blocker change reached the original: %q", original.Blocker.Reason)
	}
	if (*Goal)(nil).Clone() != nil {
		t.Error("a nil goal must clone to nil")
	}
}

func TestRoundsLeft(t *testing.T) {
	g := testGoal()
	if got := g.RoundsLeft(); got != 6 {
		t.Errorf("RoundsLeft() = %d, want 6", got)
	}
	g.RoundsStarted = g.MaxGoalRounds
	if got := g.RoundsLeft(); got != 0 {
		t.Errorf("RoundsLeft() at the cap = %d, want 0", got)
	}
	if got := (*Goal)(nil).RoundsLeft(); got != 0 {
		t.Errorf("RoundsLeft() of a nil goal = %d, want 0", got)
	}
}
