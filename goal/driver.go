package goal

import "strings"

// State is what the continuation driver needs to know about the interface. It is
// passed in, so the decision is a pure function: the TUI builds it from its own
// fields, and the test builds it from a table.
type State struct {
	// Idle is true when no run is streaming.
	Idle bool
	// Running is true while a run owns the session.
	Running bool
	// Compressing is true while a compression owns Agent.History.
	Compressing bool
	// Dialog is true while a permission or confirmation dialog is open.
	Dialog bool
	// Input is the text currently in the input box. Anything non-blank means a
	// human is composing a message, and human work wins.
	Input string
}

// Decision is what the driver may do at one moment.
type Decision struct {
	Continue bool
	Reason   string
}

// Decide answers whether another round may start now.
//
// It is advisory: StartRound is the authority and re-checks the goal under the
// same lock that claims the round. The value of Decide is that it is pure, and
// that it names the reason — a silent driver is impossible to debug.
func Decide(g *Goal, armed bool, s State) Decision {
	switch {
	case g == nil:
		return Decision{Reason: "no goal is current"}
	case g.Phase != PhaseActive:
		return Decision{Reason: "goal is " + string(g.Phase)}
	case !armed:
		return Decision{Reason: `continuation is disarmed (a human must resume it)`}
	case g.RoundsLeft() == 0:
		return Decision{Reason: "the round cap is reached"}
	case !s.Idle || s.Running:
		return Decision{Reason: "the session is not idle"}
	case s.Compressing:
		return Decision{Reason: "compression is in progress"}
	case s.Dialog:
		return Decision{Reason: "a dialog is open"}
	case strings.TrimSpace(s.Input) != "":
		return Decision{Reason: "the input box is not empty"}
	}
	return Decision{Continue: true}
}
