package agent

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// ─── DeepSeek SSE streaming ─────────────────────────────────────

func TestDeepSeekChatStream_Text(t *testing.T) {
	input := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" World\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n"
	var textParts []string
	cb := &types.StreamCallbacks{
		OnTextDelta: func(s string) { textParts = append(textParts, s) },
	}

	provider := &DeepSeekProvider{model: "test-model"}
	result, err := provider.chatStream(context.Background(), io.NopCloser(strings.NewReader(input)), time.Now(), cb)
	if err != nil {
		t.Fatalf("chatStream error: %v", err)
	}
	if result.Content != "Hello World" {
		t.Fatalf("expected Content 'Hello World', got %q", result.Content)
	}
	if len(textParts) != 2 || textParts[0] != "Hello" || textParts[1] != " World" {
		t.Fatalf("expected 2 text deltas ['Hello', ' World'], got %v", textParts)
	}
	if len(result.ToolCalls) != 0 {
		t.Fatalf("expected 0 tool calls, got %d", len(result.ToolCalls))
	}
}

func TestDeepSeekChatStream_Reasoning(t *testing.T) {
	input := "" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"\\u601d\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"\\u8003\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"result\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n"
	var reasoningParts []string
	var textParts []string
	cb := &types.StreamCallbacks{
		OnReasoningDelta: func(s string) { reasoningParts = append(reasoningParts, s) },
		OnTextDelta:      func(s string) { textParts = append(textParts, s) },
	}

	provider := &DeepSeekProvider{model: "test-model"}
	result, err := provider.chatStream(context.Background(), io.NopCloser(strings.NewReader(input)), time.Now(), cb)
	if err != nil {
		t.Fatalf("chatStream error: %v", err)
	}
	if result.ReasoningContent != "思考" {
		t.Fatalf("expected ReasoningContent '思考', got %q", result.ReasoningContent)
	}
	if result.Content != "result" {
		t.Fatalf("expected Content 'result', got %q", result.Content)
	}
	if len(reasoningParts) != 2 {
		t.Fatalf("expected 2 reasoning deltas, got %d", len(reasoningParts))
	}
}

func TestDeepSeekChatStream_ToolCalls(t *testing.T) {
	input := "" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"path\\\":\\\"main.go\\\"}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n"
	provider := &DeepSeekProvider{model: "test-model"}
	result, err := provider.chatStream(context.Background(), io.NopCloser(strings.NewReader(input)), time.Now(), nil)
	if err != nil {
		t.Fatalf("chatStream error: %v", err)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Name != "read_file" {
		t.Fatalf("expected tool name 'read_file', got %q", result.ToolCalls[0].Name)
	}
	if result.ToolCalls[0].Arguments != `{"path":"main.go"}` {
		t.Fatalf("expected args '{\"path\":\"main.go\"}', got %q", result.ToolCalls[0].Arguments)
	}
}

func TestDeepSeekChatStream_EmptyResponse(t *testing.T) {
	input := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"\"}}]}\n\n" +
		"data: [DONE]\n"
	provider := &DeepSeekProvider{model: "test-model"}
	result, err := provider.chatStream(context.Background(), io.NopCloser(strings.NewReader(input)), time.Now(), nil)
	if err != nil {
		t.Fatalf("chatStream error: %v", err)
	}
	if result.Content != "" {
		t.Fatalf("expected empty Content, got %q", result.Content)
	}
	if len(result.ToolCalls) != 0 {
		t.Fatalf("expected 0 tool calls, got %d", len(result.ToolCalls))
	}
}

func TestDeepSeekChatStream_MultipleToolCalls(t *testing.T) {
	input := "" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":1,\"id\":\"c2\",\"function\":{\"name\":\"bash\",\"arguments\":\"\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"path\\\":\\\"m.go\\\"}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":1,\"function\":{\"arguments\":\"{\\\"cmd\\\":\\\"ls\\\"}\"}}]}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n" +
		"data: [DONE]\n"
	provider := &DeepSeekProvider{model: "test-model"}
	result, err := provider.chatStream(context.Background(), io.NopCloser(strings.NewReader(input)), time.Now(), nil)
	if err != nil {
		t.Fatalf("chatStream error: %v", err)
	}
	if len(result.ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(result.ToolCalls))
	}
	if result.ToolCalls[0].Name != "read_file" || result.ToolCalls[0].Arguments != `{"path":"m.go"}` {
		t.Fatalf("tool[0] wrong: name=%q args=%q", result.ToolCalls[0].Name, result.ToolCalls[0].Arguments)
	}
	if result.ToolCalls[1].Name != "bash" || result.ToolCalls[1].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool[1] wrong: name=%q args=%q", result.ToolCalls[1].Name, result.ToolCalls[1].Arguments)
	}
}

// ─── Ollama streaming ──────────────────────────────────────────

func TestOllamaStream_Text(t *testing.T) {
	input := "" +
		"{\"message\":{\"content\":\"Hello\"}}\n" +
		"{\"message\":{\"content\":\" World\"}}\n" +
		"{\"done\":true}\n"
	var textParts []string
	cb := &types.StreamCallbacks{
		OnTextDelta: func(s string) { textParts = append(textParts, s) },
	}

	provider := &OllamaProvider{model: "test-model"}
	result, err := provider.ollamaStream(context.Background(), io.NopCloser(strings.NewReader(input)), cb)
	if err != nil {
		t.Fatalf("ollamaStream error: %v", err)
	}
	if result.Content != "Hello World" {
		t.Fatalf("expected Content 'Hello World', got %q", result.Content)
	}
	if len(textParts) != 2 {
		t.Fatalf("expected 2 text deltas, got %d", len(textParts))
	}
}

func TestOllamaStream_Thinking(t *testing.T) {
	input := "" +
		"{\"message\":{\"thinking\":\"\\u601d\\u8003\",\"content\":\"\"}}\n" +
		"{\"message\":{\"content\":\"result\"}}\n" +
		"{\"done\":true}\n"
	var reasoningParts []string
	cb := &types.StreamCallbacks{
		OnReasoningDelta: func(s string) { reasoningParts = append(reasoningParts, s) },
	}

	provider := &OllamaProvider{model: "test-model"}
	result, err := provider.ollamaStream(context.Background(), io.NopCloser(strings.NewReader(input)), cb)
	if err != nil {
		t.Fatalf("ollamaStream error: %v", err)
	}
	if result.ReasoningContent != "思考" {
		t.Fatalf("expected ReasoningContent '思考', got %q", result.ReasoningContent)
	}
	if len(reasoningParts) != 1 {
		t.Fatalf("expected 1 reasoning delta, got %d", len(reasoningParts))
	}
}

func TestOllamaStream_Empty(t *testing.T) {
	input := "" +
		"{\"message\":{\"content\":\"\"}}\n" +
		"{\"done\":true,\"done_reason\":\"length\"}\n"
	provider := &OllamaProvider{model: "test-model"}
	result, err := provider.ollamaStream(context.Background(), io.NopCloser(strings.NewReader(input)), nil)
	if err != nil {
		t.Fatalf("ollamaStream error: %v", err)
	}
	if result.Content != "" {
		t.Fatalf("expected empty Content, got %q", result.Content)
	}
}

// ─── Agent empty-response handling ─────────────────────────────

func TestAgentEmptyResponseRetry(t *testing.T) {
	callCount := 0
	provider := &MockProvider{
		chatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			callCount++
			if callCount == 1 {
				return &types.ChatResponse{Content: "", ToolCalls: nil}, nil
			}
			return &types.ChatResponse{Content: "final answer"}, nil
		},
	}

	agent := &Agent{
		Provider:     provider,
		MaxSteps:     5,
		MaxTokens:    4096,
		SystemPrompt: "test",
	}

	result, err := agent.Run(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result != "final answer" {
		t.Fatalf("expected 'final answer', got %q", result)
	}
	if callCount != 2 {
		t.Fatalf("expected 2 Chat calls, got %d", callCount)
	}
}

func TestAgentEmptyResponseExhausted(t *testing.T) {
	provider := &MockProvider{
		chatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{Content: "", ToolCalls: nil}, nil
		},
	}

	agent := &Agent{
		Provider:     provider,
		MaxSteps:     3,
		MaxTokens:    4096,
		SystemPrompt: "test",
	}

	_, err := agent.Run(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error (exceeded max steps), got nil")
	}
}
