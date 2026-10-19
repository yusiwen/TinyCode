package types

import "context"

// GoalRunInfo attributes one run to the goal it serves, for the tools that must
// know what the run may do and for the run record that has to say so.
//
// Two facts travel together and neither is derivable from anything else: which
// goal this run is working for, and whether a human started it or the
// continuation driver did. The distinction is about authority:
//
//   - A goal may be created, edited, paused or resumed only in a turn a human
//     started. A goal round that could rewrite its own objective would be a
//     machine deciding what it should work on next.
//   - A goal round may read the goal, complete it, or record a blocker, because
//     that is how the work it was given ends.
//   - A sub-agent may do none of it (see InSubagent). It was handed a unit of
//     work inside a turn, not authority over the session's objective.
type GoalRunInfo struct {
	// GoalID names the goal the run is working for.
	GoalID string `json:"goal_id"`
	// Round is the round the run started (1-based). A human turn carries the
	// current count instead, because it did not start a round of its own.
	Round int `json:"round,omitempty"`
	// Source is GoalSourceDriver or GoalSourceHuman.
	Source string `json:"source"`
}

// Sources a run can have with respect to a goal.
const (
	GoalSourceDriver = "driver"
	GoalSourceHuman  = "human"
)

type (
	goalRunKey   struct{}
	subagentKey  struct{}
)

// WithGoalRound returns a context that attributes the run to info.
func WithGoalRound(ctx context.Context, info GoalRunInfo) context.Context {
	return context.WithValue(ctx, goalRunKey{}, info)
}

// GoalRoundFrom reports the goal attribution stamped on ctx, if any.
func GoalRoundFrom(ctx context.Context) (GoalRunInfo, bool) {
	if ctx == nil {
		return GoalRunInfo{}, false
	}
	info, ok := ctx.Value(goalRunKey{}).(GoalRunInfo)
	return info, ok
}

// InGoalRound reports whether this run is an automatic goal round — the one
// source that may not rewrite the goal.
func InGoalRound(ctx context.Context) bool {
	info, ok := GoalRoundFrom(ctx)
	return ok && info.Source == GoalSourceDriver
}

// WithSubagent marks ctx as belonging to a delegated agent.
func WithSubagent(ctx context.Context) context.Context {
	return context.WithValue(ctx, subagentKey{}, true)
}

// InSubagent reports whether this run belongs to a delegated agent.
func InSubagent(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	sub, _ := ctx.Value(subagentKey{}).(bool)
	return sub
}
