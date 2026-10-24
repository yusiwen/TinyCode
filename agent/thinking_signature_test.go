package agent

import (
	"context"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// TestTheAgentCarriesAReasoningSignature: a vendor that signs its thinking block
// rejects the next step when the block comes back unsigned, so the agent has to
// copy ReasoningSignature from the response onto the assistant message it replays.
// The second request is the assertion: it is the one that carries the tool result
// after a thinking-enabled tool call.
func TestTheAgentCarriesAReasoningSignature(t *testing.T) {
	var calls [][]types.Message
	mock := &MockProvider{ChatFunc: func(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
		msgs := make([]types.Message, len(req.Messages))
		copy(msgs, req.Messages)
		calls = append(calls, msgs)
		if len(calls) == 1 {
			return &types.ChatResponse{
				ReasoningContent:   "plan",
				ReasoningSignature: "sig-1",
				ToolCalls: []types.ToolCall{{
					ID: "tool_1", Name: "echo", Arguments: `{"text":"hi"}`,
				}},
			}, nil
		}
		return &types.ChatResponse{Content: "done"}, nil
	}}

	a := New(mock)
	a.MaxSteps = 3
	a.AddTool(Tool{
		Name:        "echo",
		Description: "echo",
		Parameters:  map[string]any{"type": "object"},
		Execute:     func(context.Context, map[string]any) (string, error) { return "ok", nil },
	})

	out, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out != "done" {
		t.Fatalf("answer = %q", out)
	}
	if len(calls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(calls))
	}

	var assistant *types.Message
	for i := range calls[1] {
		if calls[1][i].Role == types.RoleAssistant && len(calls[1][i].ToolCalls) > 0 {
			assistant = &calls[1][i]
			break
		}
	}
	if assistant == nil {
		t.Fatalf("the second request carried no assistant tool-call message: %+v", calls[1])
	}
	if assistant.ReasoningContent != "plan" || assistant.ReasoningSignature != "sig-1" {
		t.Fatalf("replayed reasoning = %q signature = %q, want plan/sig-1",
			assistant.ReasoningContent, assistant.ReasoningSignature)
	}
}
