//go:build !no_ollama

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

// TestOllamaChatBatch covers the non-streaming Ollama path: endpoint, model,
// the folding of tool results into user messages and the response mapping.
func TestOllamaChatBatch(t *testing.T) {
	var gotPath string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"message":{"role":"assistant","content":"hi","thinking":"why",`+
			`"tool_calls":[{"function":{"name":"bash","arguments":{"cmd":"ls"}}}]}}`)
	}))
	defer srv.Close()

	p := NewOllamaProvider(srv.URL, "llama3")
	if p.Name() != "ollama/llama3" {
		t.Fatalf("Name = %q", p.Name())
	}

	resp, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{
			{Role: types.RoleUser, Content: "run it"},
			{Role: types.RoleTool, Name: "read_file", Content: "file body"},
		},
		Tools: []types.ToolDef{{Name: "bash", Description: "run", Parameters: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotPath != "/api/chat" {
		t.Fatalf("request path = %q", gotPath)
	}
	if gotBody["model"] != "llama3" {
		t.Fatalf("model = %v", gotBody["model"])
	}
	if gotBody["stream"] != false {
		t.Fatalf("stream = %v, want false without callbacks", gotBody["stream"])
	}
	if _, ok := gotBody["tools"]; !ok {
		t.Error("tools were not sent")
	}
	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("sent %d messages, want 2", len(msgs))
	}
	toolMsg, _ := msgs[1].(map[string]any)
	if toolMsg["role"] != types.RoleUser {
		t.Errorf("tool result role = %v, want user", toolMsg["role"])
	}
	if content, _ := toolMsg["content"].(string); !strings.Contains(content, "read_file") {
		t.Errorf("tool result content = %v, want the tool name", toolMsg["content"])
	}

	if resp.Content != "hi" || resp.ReasoningContent != "why" {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "bash" || resp.ToolCalls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
}

// TestOllamaChatStream covers the newline-delimited streaming path.
func TestOllamaChatStream(t *testing.T) {
	lines := []string{
		`{"message":{"role":"assistant","thinking":"t1"}}`,
		`{"message":{"role":"assistant","content":"He"}}`,
		"not json",
		`{"message":{"role":"assistant","content":"llo"}}`,
		`{"message":{"tool_calls":[{"function":{"name":"bash","arguments":{"cmd":"ls"}}}]}}`,
		`{"done":true}`,
	}
	stream := strings.Join(lines, "\n") + "\n"

	var gotStream bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] == true {
			gotStream = true
		}
		io.WriteString(w, stream)
	}))
	defer srv.Close()

	var reasoning, text []string
	p := NewOllamaProvider(srv.URL, "llama3")
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
	if got := strings.Join(reasoning, ""); got != "t1" {
		t.Errorf("reasoning deltas = %q", got)
	}
	if got := strings.Join(text, ""); got != "Hello" {
		t.Errorf("text deltas = %q", got)
	}
	if resp.Content != "Hello" || resp.ReasoningContent != "t1" {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "bash" || resp.ToolCalls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
}

// TestOllamaErrors covers the failure paths: a non-200 answer and a body that is
// not valid JSON.
func TestOllamaErrors(t *testing.T) {
	status := stubProviderServer(t, http.StatusInternalServerError, "model not found")
	if _, err := NewOllamaProvider(status.URL, "m").Chat(context.Background(), types.ChatRequest{}); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("status error = %v, want it to mention 500", err)
	}

	badJSON := stubProviderServer(t, http.StatusOK, "not json")
	if _, err := NewOllamaProvider(badJSON.URL, "m").Chat(context.Background(), types.ChatRequest{}); err == nil || !strings.Contains(err.Error(), "decode") {
		t.Errorf("invalid JSON = %v", err)
	}

	// An empty base URL means the local default, not an empty endpoint.
	if p := NewOllamaProvider("", "m"); !strings.HasPrefix(p.baseURL, "http://localhost:") {
		t.Errorf("default base URL = %q", p.baseURL)
	}
}
