package types

import "context"

// Two facts a run has to carry for the goal tools, both stamped on the context
// the tool receives.
//
// The distinction is about authority, not about usefulness:
//
//   - A goal may be created, edited, paused or resumed only in a turn a human
//     started. A goal round that could rewrite its own objective would be a
//     machine deciding what it should work on next.
//   - A goal round may read the goal, complete it, or record a blocker, because
//     that is how the work it was given ends.
//   - A sub-agent may do none of it. It was handed a unit of work inside a turn,
//     not authority over the session's objective; it reports back and the parent
//     decides.
type (
	goalRoundKey struct{}
	subagentKey  struct{}
)

// WithGoalRound marks ctx as one the continuation driver started.
func WithGoalRound(ctx context.Context) context.Context {
	return context.WithValue(ctx, goalRoundKey{}, true)
}

// InGoalRound reports whether this run is an automatic goal round.
func InGoalRound(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	round, _ := ctx.Value(goalRoundKey{}).(bool)
	return round
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
