package goal

import (
	"strings"
	"testing"
)

func idleState() State {
	return State{Idle: true}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name     string
		goal     func() *Goal
		armed    bool
		state    State
		cont     bool
		reasonIn string
	}{
		{
			name: "an idle session with an active, armed goal continues",
			goal: func() *Goal { g := testGoal(); return g }, armed: true, state: idleState(),
			cont: true,
		},
		{
			name: "no goal", goal: func() *Goal { return nil }, armed: false, state: idleState(),
			reasonIn: "no goal",
		},
		{
			name: "paused", goal: func() *Goal { g := testGoal(); g.Phase = PhasePaused; return g }, armed: true, state: idleState(),
			reasonIn: "paused",
		},
		{
			name: "blocked", goal: func() *Goal {
				g := testGoal()
				g.Phase = PhaseBlocked
				g.Blocker = &Blocker{Code: BlockerRoundLimit, Reason: "spent"}
				return g
			}, armed: true, state: idleState(),
			reasonIn: "blocked",
		},
		{
			name: "disarmed", goal: func() *Goal { return testGoal() }, armed: false, state: idleState(),
			reasonIn: "disarmed",
		},
		{
			name: "cap reached", goal: func() *Goal { g := testGoal(); g.RoundsStarted = g.MaxGoalRounds; return g }, armed: true, state: idleState(),
			reasonIn: "round cap",
		},
		{
			name: "a run is streaming", goal: func() *Goal { return testGoal() }, armed: true,
			state:    State{Idle: false, Running: true},
			reasonIn: "not idle",
		},
		{
			name: "not idle", goal: func() *Goal { return testGoal() }, armed: true,
			state:    State{Idle: false},
			reasonIn: "not idle",
		},
		{
			name: "compressing", goal: func() *Goal { return testGoal() }, armed: true,
			state:    State{Idle: true, Compressing: true},
			reasonIn: "compression",
		},
		{
			name: "a dialog is open", goal: func() *Goal { return testGoal() }, armed: true,
			state:    State{Idle: true, Dialog: true},
			reasonIn: "dialog",
		},
		{
			name: "the human is typing", goal: func() *Goal { return testGoal() }, armed: true,
			state:    State{Idle: true, Input: "half a thought"},
			reasonIn: "input box",
		},
		{
			name: "whitespace in the input box is not typing", goal: func() *Goal { return testGoal() }, armed: true,
			state: State{Idle: true, Input: "   \n"}, cont: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.goal(), tc.armed, tc.state)
			if got.Continue != tc.cont {
				t.Fatalf("Continue = %v (%q), want %v", got.Continue, got.Reason, tc.cont)
			}
			if tc.cont && got.Reason != "" {
				t.Errorf("a continuing decision carries a reason: %q", got.Reason)
			}
			if !tc.cont {
				if got.Reason == "" {
					t.Fatal("a refusal must say why")
				}
				if !strings.Contains(got.Reason, tc.reasonIn) {
					t.Errorf("reason = %q, want it to contain %q", got.Reason, tc.reasonIn)
				}
			}
		})
	}
}
