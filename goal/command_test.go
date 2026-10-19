package goal

import (
	"strings"
	"testing"
)

func TestParseCommand(t *testing.T) {
	cases := []struct {
		input     string
		action    Action
		objective string
		errCode   string
	}{
		{input: "", action: ActionShow},
		{input: "   ", action: ActionShow},
		{input: "pause", action: ActionPause},
		{input: "resume", action: ActionResume},
		{input: "clear", action: ActionClear},
		{input: "fix the failing test", action: ActionCreate, objective: "fix the failing test"},
		// A control word only counts when it fills the input; otherwise the
		// whole text is the objective.
		{input: "pause after verification", action: ActionCreate, objective: "pause after verification"},
		{input: "clear the cache first", action: ActionCreate, objective: "clear the cache first"},
		{input: "edit the parser to accept tabs", action: ActionEdit, objective: "the parser to accept tabs"},
		{input: "edit", errCode: ErrInvalid},
		{input: "edit   ", errCode: ErrInvalid},
		{input: "edit\t", errCode: ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			cmd, err := ParseCommand(tc.input)
			if tc.errCode != "" {
				if err == nil {
					t.Fatalf("ParseCommand(%q) = %+v, want %s", tc.input, cmd, tc.errCode)
				}
				requireCode(t, err, tc.errCode)
				return
			}
			if err != nil {
				t.Fatalf("ParseCommand(%q): %v", tc.input, err)
			}
			if cmd.Action != tc.action {
				t.Errorf("action = %q, want %q", cmd.Action, tc.action)
			}
			if cmd.Objective != tc.objective {
				t.Errorf("objective = %q, want %q", cmd.Objective, tc.objective)
			}
		})
	}
}

// TestDescribe pins the lines the TUI and the PTY scenario read. "disarmed" must
// appear for an active goal nobody has re-armed; a blocked goal must name the
// code and the reason.
func TestDescribe(t *testing.T) {
	t.Run("no goal", func(t *testing.T) {
		got := Describe(nil, false)
		if !strings.Contains(got, "No goal is current") || !strings.Contains(got, "/goal <objective>") {
			t.Errorf("Describe(nil) = %q", got)
		}
	})

	t.Run("active and armed", func(t *testing.T) {
		g := testGoal()
		got := Describe(g, true)
		for _, want := range []string{"Goal: make the failing test pass", "Round: 2/8", "Phase: active, armed"} {
			if !strings.Contains(got, want) {
				t.Errorf("Describe() = %q, missing %q", got, want)
			}
		}
		if strings.Contains(got, "disarmed") {
			t.Errorf("an armed goal says disarmed: %q", got)
		}
	})

	t.Run("active but disarmed", func(t *testing.T) {
		got := Describe(testGoal(), false)
		if !strings.Contains(got, "disarmed") || !strings.Contains(got, "/goal resume") {
			t.Errorf("Describe() = %q, want the disarmed hint", got)
		}
	})

	t.Run("blocked", func(t *testing.T) {
		g := testGoal()
		g.Phase = PhaseBlocked
		g.Blocker = &Blocker{Code: BlockerRoundLimit, Reason: "the goal spent its 8 rounds"}
		got := Describe(g, false)
		for _, want := range []string{"Phase: blocked", "Blocker: round-limit — the goal spent its 8 rounds"} {
			if !strings.Contains(got, want) {
				t.Errorf("Describe() = %q, missing %q", got, want)
			}
		}
	})

	t.Run("complete", func(t *testing.T) {
		g := testGoal()
		g.Phase = PhaseComplete
		got := Describe(g, false)
		if !strings.Contains(got, "Phase: complete") {
			t.Errorf("Describe() = %q", got)
		}
	})
}
