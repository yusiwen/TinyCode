package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// recordingSession is the smallest SessionStore that can prove a turn was
// written: the messages in order, and whether a flush happened.
type recordingSession struct {
	msgs    []types.Message
	flushes int
}

func (s *recordingSession) Append(m types.Message) error {
	s.msgs = append(s.msgs, m)
	return nil
}

func (s *recordingSession) Flush() error {
	s.flushes++
	return nil
}

// refusalResult is a terminal refusal: the kind nothing the model tries can
// change, which is what makes the loop stop instead of handing it back.
const refusalResult = "[SANDBOX] path \"/etc/passwd\": is outside the sandbox\nmode: read-only\nask: policy"

// lastTurn returns the last user/assistant pair in the history, or nil when the
// history does not end with one.
func lastTurn(history []types.Message) []types.Message {
	if len(history) < 2 {
		return nil
	}
	tail := history[len(history)-2:]
	if tail[0].Role != types.RoleUser || tail[1].Role != types.RoleAssistant {
		return nil
	}
	return tail
}

// TestStepLimitTurnIsRecorded is the case the defect hurt most (issue #258): a
// run stopped by the step limit is exactly the run that needs continuing, and
// it used to leave nothing in History or in the session.
func TestStepLimitTurnIsRecorded(t *testing.T) {
	provider := &MockProvider{ChatFunc: func(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
		if len(req.Tools) > 0 {
			return &types.ChatResponse{ToolCalls: []types.ToolCall{
				{ID: "c1", Name: "bash", Arguments: `{"command":"echo hi"}`},
			}}, nil
		}
		return &types.ChatResponse{Content: "summary of work done"}, nil
	}}
	store := &recordingSession{}
	a := &Agent{
		Provider: provider, MaxSteps: 2, MaxTokens: 100, SystemPrompt: "test",
		Tools: []Tool{echoTool()}, SessionStore: store,
	}

	if _, err := a.Run(context.Background(), "do work"); err != nil {
		t.Fatalf("run: %v", err)
	}

	turn := lastTurn(a.History)
	if turn == nil || turn[0].Content != "do work" || turn[1].Content != "summary of work done" {
		t.Fatalf("the step-limit turn is not in history: %+v", a.History)
	}
	if len(store.msgs) < 2 || store.msgs[len(store.msgs)-2].Content != "do work" ||
		store.msgs[len(store.msgs)-1].Content != "summary of work done" {
		t.Errorf("the step-limit turn is not in the session: %+v", store.msgs)
	}
	if store.flushes == 0 {
		t.Error("the session was never flushed, so the turn is only in memory")
	}

	// The next run must see it: this is the whole point of recording it.
	var seen []types.Message
	provider.ChatFunc = func(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
		seen = append(seen, req.Messages...)
		return &types.ChatResponse{Content: "next answer"}, nil
	}
	if _, err := a.Run(context.Background(), "continue"); err != nil {
		t.Fatalf("second run: %v", err)
	}
	order := []string{"do work", "summary of work done", "continue"}
	last := -1
	for _, want := range order {
		at := -1
		for i, msg := range seen {
			if msg.Content == want && i > last {
				at = i
				break
			}
		}
		if at < 0 {
			t.Fatalf("%q is missing from the next request, or out of order: %+v", want, seen)
		}
		last = at
	}
}

// TestBudgetTurnIsRecorded covers the second early return: the run stopped at a
// budget is a run worth continuing, and it must be readable afterwards.
func TestBudgetTurnIsRecorded(t *testing.T) {
	provider := &MockProvider{ChatFunc: func(_ context.Context, _ types.ChatRequest) (*types.ChatResponse, error) {
		return &types.ChatResponse{
			ToolCalls: []types.ToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"echo hi"}`}},
			Usage:     &types.Usage{PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60},
		}, nil
	}}
	store := &recordingSession{}
	a := &Agent{
		Provider: provider, MaxSteps: 10, MaxTokens: 100, SystemPrompt: "test",
		Tools: []Tool{echoTool()}, SessionStore: store,
		BudgetTokensPerRun: 100,
	}

	result, err := a.Run(context.Background(), "spend too much")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(result, "run token budget") {
		t.Fatalf("the run did not stop at the budget: %q", result)
	}
	turn := lastTurn(a.History)
	if turn == nil || turn[1].Content != result {
		t.Fatalf("the budget stop is not in history: %+v", a.History)
	}
	if len(store.msgs) < 2 || store.msgs[len(store.msgs)-1].Content != result {
		t.Errorf("the budget stop is not in the session: %+v", store.msgs)
	}
}

// TestBlockedTurnIsRecorded covers the third early return: a terminal refusal
// ends the run, and the next run has to be able to read why.
func TestBlockedTurnIsRecorded(t *testing.T) {
	refusing := Tool{
		Name: "bash", Description: "stub",
		Parameters: map[string]any{"type": "object"},
		Execute: func(context.Context, map[string]any) (string, error) {
			return refusalResult, nil
		},
	}
	provider := &MockProvider{ChatFunc: func(_ context.Context, _ types.ChatRequest) (*types.ChatResponse, error) {
		return &types.ChatResponse{ToolCalls: []types.ToolCall{
			{ID: "c1", Name: "bash", Arguments: `{"command":"cat /etc/passwd"}`},
		}}, nil
	}}
	store := &recordingSession{}
	a := &Agent{
		Provider: provider, MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test",
		Tools: []Tool{refusing}, SessionStore: store,
	}

	result, err := a.Run(context.Background(), "read the forbidden file")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result != refusalResult {
		t.Fatalf("the run did not stop at the refusal: %q", result)
	}
	turn := lastTurn(a.History)
	if turn == nil || turn[0].Content != "read the forbidden file" || turn[1].Content != refusalResult {
		t.Fatalf("the blocked turn is not in history: %+v", a.History)
	}
	if len(store.msgs) < 2 || store.msgs[len(store.msgs)-1].Content != refusalResult {
		t.Errorf("the blocked turn is not in the session: %+v", store.msgs)
	}
}
