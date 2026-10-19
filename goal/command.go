package goal

import (
	"fmt"
	"strings"
)

// Action is one operation a /goal input asks for.
type Action string

const (
	// ActionShow prints the current goal.
	ActionShow Action = "show"
	// ActionCreate starts a new objective.
	ActionCreate Action = "create"
	// ActionEdit replaces the objective without changing the phase.
	ActionEdit Action = "edit"
	// ActionPause stops automatic continuation.
	ActionPause Action = "pause"
	// ActionResume restarts or rearms the goal.
	ActionResume Action = "resume"
	// ActionClear removes the goal.
	ActionClear Action = "clear"
)

// Command is one parsed /goal input.
type Command struct {
	Action    Action
	Objective string
}

// controlWords are recognised only when they fill the whole input. Any other
// suffix is an objective, so "/goal pause after verification" creates that
// literal objective instead of pausing and discarding the rest.
var controlWords = map[string]Action{
	"pause":  ActionPause,
	"resume": ActionResume,
	"clear":  ActionClear,
}

// ParseCommand parses the text after "/goal": an empty input shows the goal, a
// bare control word performs it, "edit <objective>" replaces the objective, and
// anything else creates a goal with that objective.
func ParseCommand(rest string) (Command, error) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return Command{Action: ActionShow}, nil
	}
	if action, ok := controlWords[rest]; ok {
		return Command{Action: action}, nil
	}
	if rest == "edit" {
		return Command{}, newError(ErrInvalid, "edit needs an objective: /goal edit <objective>")
	}
	if strings.HasPrefix(rest, "edit ") {
		objective := strings.TrimSpace(strings.TrimPrefix(rest, "edit "))
		if objective == "" {
			return Command{}, newError(ErrInvalid, "edit needs an objective: /goal edit <objective>")
		}
		return Command{Action: ActionEdit, Objective: objective}, nil
	}
	return Command{Action: ActionCreate, Objective: rest}, nil
}

// Describe renders the goal for the interface. The wording is pinned by tests
// because the PTY scenario reads these lines: a resumed session must say
// "disarmed" in a way a reader (and a test) can find.
func Describe(g *Goal, armed bool) string {
	if g == nil {
		return "No goal is current. /goal <objective> creates one."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Goal: %s\n", g.Objective)
	fmt.Fprintf(&b, "Round: %d/%d\n", g.RoundsStarted, g.MaxGoalRounds)
	switch {
	case g.Phase == PhaseComplete:
		fmt.Fprintf(&b, "Phase: complete\n")
	case g.Blocker != nil:
		fmt.Fprintf(&b, "Phase: %s\n", g.Phase)
		fmt.Fprintf(&b, "Blocker: %s — %s\n", g.Blocker.Code, g.Blocker.Reason)
	default:
		state := `disarmed (say "continue" or /goal resume)`
		if armed {
			state = "armed"
		}
		fmt.Fprintf(&b, "Phase: %s, %s\n", g.Phase, state)
	}
	return strings.TrimRight(b.String(), "\n")
}
