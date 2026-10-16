package types

import (
	"context"
	"testing"
)

// TestToolCallScopeRoundTrips pins the one thing the checkpoint store relies on:
// the scope the agent loop stamps on a call comes back unchanged, and a context
// that never carried one reports none rather than an empty scope that a reader
// could mistake for a real call.
func TestToolCallScopeRoundTrips(t *testing.T) {
	want := ToolCallScope{Tool: "edit", Run: 2, Step: 7, Call: 3}
	ctx := WithToolCallScope(context.Background(), want)

	got, ok := ToolCallScopeFrom(ctx)
	if !ok {
		t.Fatal("the scope must come back")
	}
	if got != want {
		t.Errorf("scope = %+v, want %+v", got, want)
	}

	if _, ok := ToolCallScopeFrom(context.Background()); ok {
		t.Error("a plain context carries no scope")
	}
	// A nil context is what a careless caller passes; it must report "none"
	// rather than panic inside a write path.
	if _, ok := ToolCallScopeFrom(nil); ok {
		t.Error("a nil context carries no scope")
	}
}
