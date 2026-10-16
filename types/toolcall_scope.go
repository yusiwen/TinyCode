package types

import "context"

// ToolCallScope names the tool call a piece of work belongs to.
//
// The agent loop stamps it on the context it hands to a tool, so a layer below
// can attribute what it does to the call that asked for it. The checkpoint
// store is the first consumer: one manifest per call is what makes "restore the
// state before the last tool call" a statement with a subject.
//
// A caller may seed the scope before the loop runs — the TUI puts the session id
// there, which the loop cannot know — and the loop fills in the per-call fields.
// That is why the fields are split the way they are: Session belongs to whoever
// started the run, the rest to the loop.
type ToolCallScope struct {
	// Tool is the tool's registered name (`edit`, `write_file`, `write`).
	Tool string
	// Session names the conversation the run belongs to, when the caller knows
	// one. Empty is not an error: a run that has not been saved yet has no id,
	// and a call recorded without one is treated as possibly any session's.
	Session string
	// Run counts the runs this process has started, from 1. It is what keeps
	// two runs in one process from sharing a step number: both begin at step 1.
	Run int
	// Step is the ReAct step within the run, and Call the index of this call
	// within that step. Calls in one step run concurrently and carry distinct
	// indexes, which is what separates their checkpoints.
	Step int
	Call int
}

// toolCallScopeKey is unexported so only this package's constructors can set the
// value: a caller that could mint its own key would make the scope forgeable.
type toolCallScopeKey struct{}

// WithToolCallScope returns a context carrying scope.
func WithToolCallScope(ctx context.Context, scope ToolCallScope) context.Context {
	return context.WithValue(ctx, toolCallScopeKey{}, scope)
}

// ToolCallScopeFrom reports the scope stamped on ctx, if any.
func ToolCallScopeFrom(ctx context.Context) (ToolCallScope, bool) {
	if ctx == nil {
		return ToolCallScope{}, false
	}
	scope, ok := ctx.Value(toolCallScopeKey{}).(ToolCallScope)
	return scope, ok
}
