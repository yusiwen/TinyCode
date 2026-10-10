package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// budgetFixture is a provider that always asks for one more tool call, so the
// only thing that can end the run is the limit under test. It counts its calls,
// which is the property every test here asserts: *when* the loop stopped, not
// merely that it did.
func budgetFixture(spendPerCall *types.Usage, content string) (*MockProvider, *int) {
	calls := new(int)
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			*calls++
			return &types.ChatResponse{
				Content: content,
				ToolCalls: []types.ToolCall{
					{ID: "c1", Name: "bash", Arguments: `{"command":"echo hi"}`},
				},
				Usage: spendPerCall,
			}, nil
		},
	}
	return provider, calls
}

// TestBudgetStopsRunBeforeTheNextCall pins the comparison: with a run budget of N
// and M tokens per call, the provider is called exactly ceil(N/M) times. The
// third call is what an after-the-fact check would have made.
func TestBudgetStopsRunBeforeTheNextCall(t *testing.T) {
	provider, calls := budgetFixture(&types.Usage{PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60}, "")
	a := &Agent{
		Provider: provider, MaxSteps: 10, MaxTokens: 100, SystemPrompt: "test",
		Tools:              []Tool{echoTool()},
		BudgetTokensPerRun: 100,
	}

	result, err := a.Run(context.Background(), "do work")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if *calls != 2 {
		t.Errorf("provider called %d times, want 2 for a 100-token budget at 60 tokens per call", *calls)
	}
	if !strings.Contains(result, "run token budget") {
		t.Errorf("result = %q, want it to name the run budget", result)
	}
	if !strings.Contains(result, "120 of 100") {
		t.Errorf("result = %q, want the spend and the limit", result)
	}
	if !strings.Contains(result, "budget.max_tokens_per_run") {
		t.Errorf("result = %q, want it to name the knob to turn", result)
	}
}

// TestNoBudgetMakesTheSameCalls is the control for "off by default": without a
// budget the loop is bounded only by its step count, exactly as before.
func TestNoBudgetMakesTheSameCalls(t *testing.T) {
	const steps = 10
	calls := new(int)
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			*calls++
			if *calls == steps {
				return &types.ChatResponse{Content: "done"}, nil
			}
			return &types.ChatResponse{
				ToolCalls: []types.ToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"echo hi"}`}},
				Usage:     &types.Usage{PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000},
			}, nil
		},
	}
	a := &Agent{
		Provider: provider, MaxSteps: steps, MaxTokens: 100, SystemPrompt: "test",
		Tools: []Tool{echoTool()},
	}

	if _, err := a.Run(context.Background(), "do work"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if *calls != steps {
		t.Errorf("provider called %d times with no budget configured, want %d", *calls, steps)
	}
	// The nine tool-call steps reported 2000 tokens each; the final answer
	// reported nothing, so it is counted as the estimate of its own text.
	want := (steps-1)*2000 + EstimateTokens("done")
	if a.UsageTotal.TotalTokens != want {
		t.Errorf("UsageTotal = %d, want %d: the total is kept even when it is not enforced",
			a.UsageTotal.TotalTokens, want)
	}
}

// TestSessionBudgetStopsTheSecondRunBeforeAnyCall is the acceptance that the two
// limits are independent: the run that exhausts the session still finishes its
// own accounting, and the next run costs nothing at all.
func TestSessionBudgetStopsTheSecondRunBeforeAnyCall(t *testing.T) {
	provider, calls := budgetFixture(&types.Usage{PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60}, "")
	a := &Agent{
		Provider: provider, MaxSteps: 10, MaxTokens: 100, SystemPrompt: "test",
		Tools:                  []Tool{echoTool()},
		BudgetTokensPerSession: 100,
	}

	if _, err := a.Run(context.Background(), "first"); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	afterFirst := *calls
	if afterFirst != 2 {
		t.Fatalf("first run called the provider %d times, want 2", afterFirst)
	}

	result, err := a.Run(context.Background(), "second")
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if *calls != afterFirst {
		t.Errorf("the second run called the provider %d more times, want 0: the session budget is spent",
			*calls-afterFirst)
	}
	if !strings.Contains(result, "session token budget") {
		t.Errorf("result = %q, want it to name the session budget", result)
	}
	if !strings.Contains(result, "budget.max_tokens_per_session") {
		t.Errorf("result = %q, want it to name the knob to turn", result)
	}
}

// TestBudgetCountsAnEstimateWhenUsageIsUnreported keeps the boundary alive for a
// provider that reports nothing: the run must still stop, on the estimate.
func TestBudgetCountsAnEstimateWhenUsageIsUnreported(t *testing.T) {
	// 400 bytes of content is about 100 estimated tokens.
	content := strings.Repeat("x", 400)
	provider, calls := budgetFixture(nil, content)
	a := &Agent{
		Provider: provider, MaxSteps: 10, MaxTokens: 100, SystemPrompt: "test",
		Tools:              []Tool{echoTool()},
		BudgetTokensPerRun: 50,
	}

	result, err := a.Run(context.Background(), "do work")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if *calls != 1 {
		t.Errorf("provider called %d times, want 1: the estimate of the first answer already exceeds 50 tokens", *calls)
	}
	if want := EstimateTokens(content); !strings.Contains(result, fmt.Sprintf("%d of 50", want)) {
		t.Errorf("result = %q, want it to report the estimated spend %d", result, want)
	}
}

// TestBudgetEstimateDoesNotFireOnUsage fixes the other half of the same
// contract: the estimate advances the total, but a consumer is never handed an
// estimate dressed as a provider number.
func TestBudgetEstimateDoesNotFireOnUsage(t *testing.T) {
	const answer = "a plain answer with no usage behind it"
	fired := 0
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{Content: answer}, nil
		},
	}
	a := &Agent{
		Provider: provider, MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test",
		StreamCallbacks: &types.StreamCallbacks{
			OnUsage: func(u types.Usage) { fired++ },
		},
	}

	if _, err := a.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fired != 0 {
		t.Errorf("OnUsage fired %d times for a call that reported nothing", fired)
	}
	if want := EstimateTokens(answer); a.UsageTotal.TotalTokens != want {
		t.Errorf("UsageTotal = %d, want the %d-token estimate", a.UsageTotal.TotalTokens, want)
	}
}

// TestRunBudgetResetsBetweenRuns: the run limit must not accumulate, or the
// second prompt in a session would start already spent.
func TestRunBudgetResetsBetweenRuns(t *testing.T) {
	provider, calls := budgetFixture(&types.Usage{PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60}, "")
	a := &Agent{
		Provider: provider, MaxSteps: 10, MaxTokens: 100, SystemPrompt: "test",
		Tools:              []Tool{echoTool()},
		BudgetTokensPerRun: 100,
	}

	for run := 0; run < 2; run++ {
		if _, err := a.Run(context.Background(), "do work"); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
	if *calls != 4 {
		t.Errorf("provider called %d times over two runs, want 4: each run gets its own 100-token budget", *calls)
	}
	if a.runUsage.TotalTokens != 120 {
		t.Errorf("runUsage = %d, want the last run's 120", a.runUsage.TotalTokens)
	}
}
