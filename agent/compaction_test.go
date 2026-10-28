package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// runMessages builds a system prompt, a user prompt, and n rounds of an
// assistant tool call answered by a tool result — the shape the loop produces.
func runMessages(n int) []types.Message {
	messages := []types.Message{
		{Role: types.RoleSystem, Content: "system prompt"},
		{Role: types.RoleUser, Content: "do the thing"},
	}
	for i := 0; i < n; i++ {
		id := string(rune('a' + i%26))
		messages = append(messages,
			types.Message{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{
				ID: id, Name: "bash", Arguments: `{"command":"echo round-` + id + `"}`,
			}}},
			types.Message{Role: types.RoleTool, ToolCallID: id, Name: "bash", Content: "output of round " + id},
		)
	}
	return messages
}

// TestCompactRunMessagesSummarizesTheMiddle pins the S2 behaviour: the run's own
// traffic is summarized, the head and the recent tail survive, and the call that
// does it is accounted for.
func TestCompactRunMessagesSummarizesTheMiddle(t *testing.T) {
	provider := &recordingProvider{content: "RUN SUMMARY", usage: &types.Usage{TotalTokens: 42}}
	ag := New(provider)
	ag.ContextLength = 1000
	ag.CompressionThreshold = 1 // always compact

	in := runMessages(10) // 2 + 20 messages
	out, changed := ag.compactRunMessages(context.Background(), in, 4)
	if !changed {
		t.Fatal("a long run was not compacted")
	}
	if len(out) >= len(in) {
		t.Fatalf("compaction did not shrink the messages: %d -> %d", len(in), len(out))
	}
	// The head survives byte for byte, and so does the tail.
	if out[0].Content != "system prompt" || out[1].Content != "do the thing" {
		t.Fatalf("the head was altered: %+v", out[:2])
	}
	if !containsMessage(out, "RUN SUMMARY") {
		t.Fatal("the summary is missing")
	}
	tail := out[len(out)-8:]
	for i, msg := range tail {
		if msg.Role != in[len(in)-8+i].Role || msg.Content != in[len(in)-8+i].Content {
			t.Fatalf("the tail moved at %d: %+v", i, msg)
		}
	}
	// The summarizer saw the middle, and its call was counted.
	if len(provider.requests) != 1 {
		t.Fatalf("the summarizer was called %d times", len(provider.requests))
	}
	if prompt := provider.requests[0].Messages[1].Content; !strings.Contains(prompt, "called bash") {
		t.Fatalf("the summarizer did not see the middle:\n%s", prompt)
	}
	if ag.UsageTotal.TotalTokens != 42 {
		t.Fatalf("the summarizer call was not accounted: UsageTotal = %d", ag.UsageTotal.TotalTokens)
	}
}

// TestCompactRunMessagesKeepsToolCallsWithTheirResults pins the boundary rule: a
// tool_calls assistant must never be separated from the results that answer it,
// or the provider rejects the next call.
func TestCompactRunMessagesKeepsToolCallsWithTheirResults(t *testing.T) {
	provider := &recordingProvider{content: "RUN SUMMARY"}
	ag := New(provider)
	ag.ContextLength = 1000
	ag.CompressionThreshold = 1

	in := runMessages(12)
	out, changed := ag.compactRunMessages(context.Background(), in, 2)
	if !changed {
		t.Fatal("not compacted")
	}
	// Every tool message must follow the assistant call that produced it.
	pending := map[string]bool{}
	for _, msg := range out {
		switch msg.Role {
		case types.RoleAssistant:
			for _, call := range msg.ToolCalls {
				pending[call.ID] = true
			}
		case types.RoleTool:
			if !pending[msg.ToolCallID] {
				t.Fatalf("tool result %q has no call before it", msg.ToolCallID)
			}
			delete(pending, msg.ToolCallID)
		}
	}
	if len(pending) != 0 {
		t.Fatalf("assistant calls without results: %v", pending)
	}
}

// TestCompactRunMessagesDoesNothingUnderTheThreshold is the control: a short run
// costs nothing and is returned unchanged.
func TestCompactRunMessagesDoesNothingUnderTheThreshold(t *testing.T) {
	provider := &recordingProvider{content: "RUN SUMMARY"}
	ag := New(provider)
	ag.ContextLength = 1000000
	ag.CompressionThreshold = 1000000

	in := runMessages(3)
	out, changed := ag.compactRunMessages(context.Background(), in, 1)
	if changed || len(out) != len(in) {
		t.Fatalf("a short run was compacted: changed=%v len=%d", changed, len(out))
	}
	if len(provider.requests) != 0 {
		t.Fatalf("the summarizer was called %d times for nothing", len(provider.requests))
	}
}

// TestCompressHistoryAccountsForItsCall pins the pre-existing gap this closed:
// the summarizer's tokens used to be invisible to UsageTotal and the budgets.
func TestCompressHistoryAccountsForItsCall(t *testing.T) {
	provider := &recordingProvider{content: "SUMMARY", usage: &types.Usage{TotalTokens: 30}}
	ag := New(provider)
	ag.ContextLength = 1000
	ag.CompressionThreshold = 1
	ag.History = turnHistory(6)

	if _, err := ag.compressHistory(context.Background(), ag.History); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if ag.UsageTotal.TotalTokens != 30 {
		t.Fatalf("UsageTotal = %d, want the summarizer's 30", ag.UsageTotal.TotalTokens)
	}
}

// scriptedProvider answers a fixed sequence, then one final answer, recording
// every request: it is how a multi-step run is driven in a test.
type scriptedProvider struct {
	steps    []types.ChatResponse
	requests []types.ChatRequest
}

func (p *scriptedProvider) Name() string { return "scripted" }

func (p *scriptedProvider) Chat(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.requests = append(p.requests, req)
	if i := len(p.requests) - 1; i < len(p.steps) {
		resp := p.steps[i]
		return &resp, nil
	}
	return &types.ChatResponse{Content: "done"}, nil
}

// TestRunCompactsItsOwnMessages is the integration half: a long run passes
// through Run, and a later provider call carries the compaction marker. Without
// the loop hook the run would grow every step and never compact.
func TestRunCompactsItsOwnMessages(t *testing.T) {
	steps := make([]types.ChatResponse, 10)
	for i := range steps {
		steps[i] = types.ChatResponse{ToolCalls: []types.ToolCall{{
			ID: string(rune('a' + i)), Name: "echo", Arguments: "{}",
		}}}
	}
	provider := &scriptedProvider{steps: steps}
	ag := New(provider)
	ag.Tools = []Tool{{
		Name: "echo", Description: "echo", Parameters: map[string]any{"type": "object"},
		Execute: func(context.Context, map[string]any) (string, error) { return "echoed output", nil },
	}}
	ag.ContextLength = 1000
	ag.CompressionThreshold = 1 // compact as soon as a round exists
	ag.MaxSteps = 12

	if _, err := ag.Run(context.Background(), "keep going"); err != nil {
		t.Fatalf("run: %v", err)
	}
	compacted := false
	for _, req := range provider.requests {
		if containsMessage(req.Messages, "[COMPACTED RUN") {
			compacted = true
			break
		}
	}
	if !compacted {
		t.Fatalf("no provider request carried a compacted run across %d calls", len(provider.requests))
	}
}
