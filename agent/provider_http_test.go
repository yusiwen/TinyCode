package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// stubProviderServer answers every request with the given status and body.
func stubProviderServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestOpenAIChatBatch covers the non-streaming path: the endpoint, the auth
// header, the request body (model override, tools, tool-result messages) and the
// mapping of content, reasoning and tool calls back into a ChatResponse.
func TestOpenAIChatBatch(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		gotBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello",`+
			`"reasoning_content":"thinking","tool_calls":[{"id":"call_1","type":"function",`+
			`"function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}}]}}]}`)
	}))
	defer srv.Close()

	p := NewOpenAIProvider("sk-test", srv.URL, "deepseek-chat")
	if p.Name() != "openai/deepseek-chat" {
		t.Fatalf("Name = %q", p.Name())
	}

	resp, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{
			{Role: types.RoleSystem, Content: "sys"},
			{Role: types.RoleUser, Content: "hi"},
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{ID: "call_9", Name: "read_file", Arguments: "{}"}}},
			{Role: types.RoleTool, Name: "read_file", ToolCallID: "call_9", Content: "file body"},
		},
		Tools:     []types.ToolDef{{Name: "bash", Description: "run a command", Parameters: map[string]any{"type": "object"}}},
		MaxTokens: 128,
		Model:     "override-model",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotPath != "/chat/completions" {
		t.Fatalf("request path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotBody["model"] != "override-model" {
		t.Fatalf("model = %v, want the request override", gotBody["model"])
	}
	if gotBody["max_tokens"] != float64(128) {
		t.Fatalf("max_tokens = %v", gotBody["max_tokens"])
	}
	if _, ok := gotBody["tools"]; !ok {
		t.Error("tools were not sent")
	}
	if _, ok := gotBody["stream"]; ok {
		t.Error("a batch request must not ask for streaming")
	}

	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 4 {
		t.Fatalf("sent %d messages, want 4", len(msgs))
	}
	assistant, _ := msgs[2].(map[string]any)
	if _, ok := assistant["tool_calls"]; !ok {
		t.Errorf("assistant tool_calls were dropped: %v", assistant)
	}
	toolMsg, _ := msgs[3].(map[string]any)
	if toolMsg["tool_call_id"] != "call_9" || toolMsg["name"] != "read_file" {
		t.Errorf("tool result message = %v", toolMsg)
	}

	if resp.Content != "hello" || resp.ReasoningContent != "thinking" {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if tc := resp.ToolCalls[0]; tc.ID != "call_1" || tc.Name != "bash" || tc.Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool call = %+v", tc)
	}
}

// TestOpenAIChatStream covers the SSE path, including deltas split across
// events, reasoning deltas, tool calls accumulated by index and lines that must
// be ignored rather than abort the stream.
func TestOpenAIChatStream(t *testing.T) {
	events := []string{
		": keep-alive",
		`data: {"choices":[{"delta":{"role":"assistant","reasoning_content":"think-1"}}]}`,
		``,
		"data: {not json",
		``,
		`data: {"choices":[{"delta":{"content":"Hel"}}]}`,
		``,
		`data: {"choices":[{"delta":{"content":"lo"}}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"bash","arguments":"{\"cmd\":"}}]}}]}`,
		``,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]}}]}`,
		``,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		``,
		`data: [DONE]`,
		``,
	}
	stream := strings.Join(events, "\n")

	var gotStream bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			gotStream = true
		}
		if _, ok := body["stream_options"]; !ok {
			t.Error("streaming request must ask for usage")
		}
		io.WriteString(w, stream)
	}))
	defer srv.Close()

	var reasoning, text []string
	p := NewOpenAIProvider("k", srv.URL, "m")
	resp, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}},
		StreamCallbacks: &types.StreamCallbacks{
			OnReasoningDelta: func(s string) { reasoning = append(reasoning, s) },
			OnTextDelta:      func(s string) { text = append(text, s) },
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if !gotStream {
		t.Error("the request did not enable streaming")
	}
	if got := strings.Join(reasoning, ""); got != "think-1" {
		t.Errorf("reasoning deltas = %q", got)
	}
	if got := strings.Join(text, ""); got != "Hello" {
		t.Errorf("text deltas = %q", got)
	}
	if resp.Content != "Hello" || resp.ReasoningContent != "think-1" {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Arguments != `{"cmd":"ls"}` || resp.ToolCalls[0].ID != "call_a" {
		t.Fatalf("accumulated tool calls = %+v", resp.ToolCalls)
	}
}

// TestOpenAIErrors covers the failure paths that must surface an error instead
// of an empty response.
func TestOpenAIErrors(t *testing.T) {
	status := stubProviderServer(t, http.StatusInternalServerError, "upstream exploded")
	p := NewOpenAIProvider("k", status.URL, "m")
	if _, err := p.Chat(context.Background(), types.ChatRequest{}); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("status error = %v, want it to mention 500", err)
	}

	badJSON := stubProviderServer(t, http.StatusOK, "not json")
	if _, err := NewOpenAIProvider("k", badJSON.URL, "m").Chat(context.Background(), types.ChatRequest{}); err == nil || !strings.Contains(err.Error(), "parse response") {
		t.Errorf("invalid JSON = %v", err)
	}

	noChoices := stubProviderServer(t, http.StatusOK, `{"choices":[]}`)
	if _, err := NewOpenAIProvider("k", noChoices.URL, "m").Chat(context.Background(), types.ChatRequest{}); err == nil || !strings.Contains(err.Error(), "no choices") {
		t.Errorf("empty choices = %v", err)
	}

	// A cancelled context must abort the call.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewOpenAIProvider("k", noChoices.URL, "m").Chat(ctx, types.ChatRequest{}); err == nil {
		t.Error("a cancelled context must fail the call")
	}
}
