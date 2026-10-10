package agent

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// usageSSE is a full stream whose usage lives in a chunk of its own, placed
// after the finish_reason chunk — the order an OpenAI-compatible endpoint uses.
// A parser that stops at finish_reason never reads it, which is the bug this
// fixture pins.
const usageSSE = "" +
	"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
	"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
	"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"total_tokens\":18}}\n\n" +
	"data: [DONE]\n"

func TestOpenAIStreamReportsUsageAfterFinishReason(t *testing.T) {
	provider := &OpenAIProvider{model: "test-model"}
	res, err := provider.chatStream(context.Background(),
		io.NopCloser(strings.NewReader(usageSSE)), time.Now(), &types.StreamCallbacks{}, nil)
	if err != nil {
		t.Fatalf("chatStream error: %v", err)
	}
	if res.Content != "Hi" {
		t.Fatalf("Content = %q, want %q", res.Content, "Hi")
	}
	if res.Usage == nil {
		t.Fatal("usage chunk after finish_reason was dropped")
	}
	want := types.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}
	if *res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", *res.Usage, want)
	}
}

func TestOpenAIStreamWithoutUsageReportsNone(t *testing.T) {
	input := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n"
	provider := &OpenAIProvider{model: "test-model"}
	res, err := provider.chatStream(context.Background(),
		io.NopCloser(strings.NewReader(input)), time.Now(), &types.StreamCallbacks{}, nil)
	if err != nil {
		t.Fatalf("chatStream error: %v", err)
	}
	if res.Usage != nil {
		t.Fatalf("Usage = %+v, want nil when the endpoint reports none", *res.Usage)
	}
}

// TestOpenAIStreamReportsUsageWithToolCalls covers the ordering the agent loop
// actually sees: a tool-call step ends with finish_reason, then the usage chunk.
func TestOpenAIStreamReportsUsageWithToolCalls(t *testing.T) {
	input := "" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"main.go\\\"}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":40,\"completion_tokens\":3,\"total_tokens\":43}}\n\n" +
		"data: [DONE]\n"
	provider := &OpenAIProvider{model: "test-model"}
	res, err := provider.chatStream(context.Background(),
		io.NopCloser(strings.NewReader(input)), time.Now(), &types.StreamCallbacks{}, nil)
	if err != nil {
		t.Fatalf("chatStream error: %v", err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "read_file" {
		t.Fatalf("tool calls not accumulated: %+v", res.ToolCalls)
	}
	if res.Usage == nil || res.Usage.TotalTokens != 43 {
		t.Fatalf("Usage = %+v, want total 43", res.Usage)
	}
}

func TestOpenAIBatchReportsUsage(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hello"}}],` +
		`"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`
	provider := &OpenAIProvider{model: "test-model"}
	res, err := provider.chatBatch(context.Background(), io.NopCloser(strings.NewReader(body)), time.Now())
	if err != nil {
		t.Fatalf("chatBatch error: %v", err)
	}
	if res.Usage == nil {
		t.Fatal("batch usage was dropped")
	}
	want := types.Usage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7}
	if *res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", *res.Usage, want)
	}
}

// TestOpenAIUsageDerivesTotalWhenAbsent covers a compatible endpoint that
// reports the two halves but not their sum.
func TestOpenAIUsageDerivesTotalWhenAbsent(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hi"}}],` +
		`"usage":{"prompt_tokens":3,"completion_tokens":4}}`
	provider := &OpenAIProvider{model: "test-model"}
	res, err := provider.chatBatch(context.Background(), io.NopCloser(strings.NewReader(body)), time.Now())
	if err != nil {
		t.Fatalf("chatBatch error: %v", err)
	}
	if res.Usage == nil || res.Usage.TotalTokens != 7 {
		t.Fatalf("Usage = %+v, want a derived total of 7", res.Usage)
	}
}

func TestOpenAIBatchWithoutUsageReportsNone(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`
	provider := &OpenAIProvider{model: "test-model"}
	res, err := provider.chatBatch(context.Background(), io.NopCloser(strings.NewReader(body)), time.Now())
	if err != nil {
		t.Fatalf("chatBatch error: %v", err)
	}
	if res.Usage != nil {
		t.Fatalf("Usage = %+v, want nil", *res.Usage)
	}
}

// ─── Ollama ────────────────────────────────────────────────────

func TestOllamaStreamReportsUsageFromDoneLine(t *testing.T) {
	input := "" +
		"{\"message\":{\"content\":\"Hello\"}}\n" +
		"{\"done\":true,\"prompt_eval_count\":12,\"eval_count\":34}\n"
	provider := &OllamaProvider{model: "test-model"}
	res, err := provider.ollamaStream(context.Background(),
		io.NopCloser(strings.NewReader(input)), &types.StreamCallbacks{}, nil)
	if err != nil {
		t.Fatalf("ollamaStream error: %v", err)
	}
	if res.Usage == nil {
		t.Fatal("done-line counters were dropped")
	}
	want := types.Usage{PromptTokens: 12, CompletionTokens: 34, TotalTokens: 46}
	if *res.Usage != want {
		t.Fatalf("Usage = %+v, want %+v", *res.Usage, want)
	}
}

func TestOllamaStreamWithoutCountersReportsNone(t *testing.T) {
	input := "" +
		"{\"message\":{\"content\":\"Hello\"}}\n" +
		"{\"done\":true}\n"
	provider := &OllamaProvider{model: "test-model"}
	res, err := provider.ollamaStream(context.Background(),
		io.NopCloser(strings.NewReader(input)), &types.StreamCallbacks{}, nil)
	if err != nil {
		t.Fatalf("ollamaStream error: %v", err)
	}
	if res.Usage != nil {
		t.Fatalf("Usage = %+v, want nil", *res.Usage)
	}
}

func TestOllamaStreamReportsZeroCounters(t *testing.T) {
	// The fields are present and zero: that is a reported zero, not an absent
	// report, and it must stay distinguishable from one.
	input := "" +
		"{\"message\":{\"content\":\"\"}}\n" +
		"{\"done\":true,\"prompt_eval_count\":0,\"eval_count\":0}\n"
	provider := &OllamaProvider{model: "test-model"}
	res, err := provider.ollamaStream(context.Background(),
		io.NopCloser(strings.NewReader(input)), &types.StreamCallbacks{}, nil)
	if err != nil {
		t.Fatalf("ollamaStream error: %v", err)
	}
	if res.Usage == nil {
		t.Fatal("a reported zero must not become an absent report")
	}
	if *res.Usage != (types.Usage{}) {
		t.Fatalf("Usage = %+v, want the zero value", *res.Usage)
	}
}

func TestOllamaBatchReportsUsage(t *testing.T) {
	body := `{"message":{"role":"assistant","content":"hi"},` +
		`"prompt_eval_count":5,"eval_count":6}`
	provider := &OllamaProvider{model: "test-model"}
	res, err := provider.ollamaBatch(io.NopCloser(strings.NewReader(body)))
	if err != nil {
		t.Fatalf("ollamaBatch error: %v", err)
	}
	if res.Usage == nil || res.Usage.TotalTokens != 11 {
		t.Fatalf("Usage = %+v, want total 11", res.Usage)
	}
}

// ─── Agent accumulation ────────────────────────────────────────

// echoTool is a minimal tool that lets the loop take a second step.
func echoTool() Tool {
	return Tool{
		Name: "bash", Description: "run a command",
		Parameters: map[string]any{"type": "object"},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			return "ok", nil
		},
	}
}

func TestAgentAccumulatesReportedUsageAcrossSteps(t *testing.T) {
	step := 0
	var events []types.Usage
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			step++
			if step == 1 {
				return &types.ChatResponse{
					ToolCalls: []types.ToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"echo hi"}`}},
					Usage:     &types.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
				}, nil
			}
			return &types.ChatResponse{
				Content: "done",
				Usage:   &types.Usage{PromptTokens: 20, CompletionTokens: 5, TotalTokens: 25},
			}, nil
		},
	}
	a := &Agent{
		Provider: provider, MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test",
		Tools: []Tool{echoTool()},
		StreamCallbacks: &types.StreamCallbacks{
			OnUsage: func(u types.Usage) { events = append(events, u) },
		},
	}

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("OnUsage fired %d times, want once per reported call (2)", len(events))
	}
	want := types.Usage{PromptTokens: 30, CompletionTokens: 7, TotalTokens: 37}
	if a.UsageTotal != want {
		t.Fatalf("UsageTotal = %+v, want %+v", a.UsageTotal, want)
	}
	if events[0].TotalTokens != 12 || events[1].TotalTokens != 25 {
		t.Fatalf("events = %+v, want the two reported records in order", events)
	}
}

func TestAgentReportsNoUsageWhenEndpointReportsNone(t *testing.T) {
	fired := 0
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{Content: "answer"}, nil
		},
	}
	a := &Agent{
		Provider: provider, MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test",
		StreamCallbacks: &types.StreamCallbacks{
			OnUsage: func(u types.Usage) { fired++ },
		},
	}

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fired != 0 {
		t.Fatalf("OnUsage fired %d times for a call that reported nothing", fired)
	}
	if a.UsageTotal != (types.Usage{}) {
		t.Fatalf("UsageTotal = %+v, want the zero value", a.UsageTotal)
	}
}

// TestAgentCountsSummaryCallUsage pins the max-steps summary call, which builds
// its own StreamCallbacks and so is a separate path for both the total and the
// forwarded event.
func TestAgentCountsSummaryCallUsage(t *testing.T) {
	var events []types.Usage
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			if len(req.Tools) > 0 {
				return &types.ChatResponse{
					ToolCalls: []types.ToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"echo hi"}`}},
					Usage:     &types.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2},
				}, nil
			}
			return &types.ChatResponse{
				Content: "summary",
				Usage:   &types.Usage{PromptTokens: 30, CompletionTokens: 9, TotalTokens: 39},
			}, nil
		},
	}
	a := &Agent{
		Provider: provider, MaxSteps: 1, MaxTokens: 100, SystemPrompt: "test",
		Tools: []Tool{echoTool()},
		StreamCallbacks: &types.StreamCallbacks{
			OnUsage: func(u types.Usage) { events = append(events, u) },
		},
	}

	result, err := a.Run(context.Background(), "do work")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result != "summary" {
		t.Fatalf("result = %q, want the summary", result)
	}
	if len(events) != 2 {
		t.Fatalf("OnUsage fired %d times, want 2 (step + summary)", len(events))
	}
	want := types.Usage{PromptTokens: 31, CompletionTokens: 10, TotalTokens: 41}
	if a.UsageTotal != want {
		t.Fatalf("UsageTotal = %+v, want %+v", a.UsageTotal, want)
	}
}
