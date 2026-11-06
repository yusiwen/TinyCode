package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// compactionProvider answers a fixed number of tool calls and then a final
// answer, recording every request.
type compactionProvider struct {
	steps    int
	requests []types.ChatRequest
}

func (p *compactionProvider) Name() string { return "compaction-test" }

func (p *compactionProvider) Chat(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.requests = append(p.requests, req)
	if len(p.requests) <= p.steps {
		return &types.ChatResponse{ToolCalls: []types.ToolCall{{
			ID: "c" + string(rune('a'+len(p.requests))), Name: "echo", Arguments: "{}",
		}}}, nil
	}
	return &types.ChatResponse{Content: "done"}, nil
}

// TestRunCompactsItsOwnMessages: a run that crosses the threshold must not keep
// growing; a later provider request has to carry the compaction marker, and the
// run has to finish.
func TestRunCompactsItsOwnMessages(t *testing.T) {
	provider := &compactionProvider{steps: 10}
	ag := New(provider)
	ag.MaxSteps = 12
	ag.ContextLength = 1000
	ag.CompressionThreshold = 1 // compact as soon as a round exists
	ag.Tools = []Tool{{
		Name: "echo", Description: "echo", Parameters: map[string]any{"type": "object"},
		Execute: func(context.Context, map[string]any) (string, error) { return "echoed", nil },
	}}

	if _, err := ag.Run(context.Background(), "keep going"); err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, req := range provider.requests {
		for _, msg := range req.Messages {
			if strings.Contains(msg.Content, "COMPACTED RUN") {
				return
			}
		}
	}
	t.Fatalf("no provider request carried a compacted run across %d calls", len(provider.requests))
}
