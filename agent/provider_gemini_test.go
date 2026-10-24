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

// TestGeminiChatBatch covers generateContent: the model path, the API-key header,
// systemInstruction/contents mapping, functionCall/functionResponse and the
// native usage counters.
func TestGeminiChatBatch(t *testing.T) {
	var gotPath, gotKey string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		gotBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[`+
			`{"text":"thought","thought":true},{"text":"hello"},`+
			`{"functionCall":{"name":"bash","args":{"cmd":"ls"}}}]},`+
			`"finishReason":"STOP"}],`+
			`"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":20,`+
			`"thoughtsTokenCount":5,"cachedContentTokenCount":40,"totalTokenCount":125}}`)
	}))
	defer srv.Close()

	p := NewGeminiProvider("AIza-test", srv.URL, "gemini-2.5-pro")
	p.SetRouteInfo("gemini", "", "USD")
	p.SetThinkingBudget(2048)

	resp, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{
			{Role: types.RoleSystem, Content: "sys"},
			{Role: types.RoleUser, Content: "hi"},
			{Role: types.RoleAssistant, Content: "answer",
				ToolCalls: []types.ToolCall{{ID: "bash", Name: "bash", Arguments: `{"cmd":"pwd"}`}}},
			{Role: types.RoleTool, Name: "bash", ToolCallID: "bash", Content: "/tmp"},
		},
		Tools: []types.ToolDef{{Name: "bash", Description: "run", Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{"type": "string"},
			},
			"required": []string{"command"},
		}}},
		MaxTokens: 128,
		Model:     "gemini-2.5-flash",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotPath != "/models/gemini-2.5-flash:generateContent" {
		t.Fatalf("request path = %q", gotPath)
	}
	if gotKey != "AIza-test" {
		t.Fatalf("x-goog-api-key = %q", gotKey)
	}

	sys, _ := gotBody["systemInstruction"].(map[string]any)
	parts, _ := sys["parts"].([]any)
	if len(parts) != 1 || parts[0].(map[string]any)["text"] != "sys" {
		t.Fatalf("systemInstruction = %v", sys)
	}

	contents, _ := gotBody["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("contents = %d, want 3", len(contents))
	}
	first := contents[0].(map[string]any)
	if first["role"] != "user" {
		t.Fatalf("first content = %v", first)
	}
	assistant := contents[1].(map[string]any)
	if assistant["role"] != "model" {
		t.Fatalf("assistant role = %v", assistant["role"])
	}
	aparts, _ := assistant["parts"].([]any)
	if len(aparts) != 2 {
		t.Fatalf("assistant parts = %v", aparts)
	}
	call, _ := aparts[1].(map[string]any)["functionCall"].(map[string]any)
	if call["name"] != "bash" {
		t.Fatalf("functionCall = %v", call)
	}
	tool := contents[2].(map[string]any)
	tparts, _ := tool["parts"].([]any)
	fr, _ := tparts[0].(map[string]any)["functionResponse"].(map[string]any)
	if fr["name"] != "bash" {
		t.Fatalf("functionResponse = %v", fr)
	}
	if result := fr["response"].(map[string]any)["result"]; result != "/tmp" {
		t.Fatalf("functionResponse result = %v", result)
	}

	tools, _ := gotBody["tools"].([]any)
	decls := tools[0].(map[string]any)["functionDeclarations"].([]any)
	if len(decls) != 1 || decls[0].(map[string]any)["name"] != "bash" {
		t.Fatalf("functionDeclarations = %v", decls)
	}
	params := decls[0].(map[string]any)["parameters"].(map[string]any)
	if params["type"] != "OBJECT" {
		t.Fatalf("schema type = %v, want Gemini's upper-case OBJECT", params["type"])
	}
	props := params["properties"].(map[string]any)
	if props["command"].(map[string]any)["type"] != "STRING" {
		t.Fatalf("nested property type = %v, want STRING", props["command"])
	}
	gen, _ := gotBody["generationConfig"].(map[string]any)
	if gen["maxOutputTokens"] != float64(128) {
		t.Fatalf("generationConfig = %v", gen)
	}
	thinking, _ := gen["thinkingConfig"].(map[string]any)
	if thinking["includeThoughts"] != true || thinking["thinkingBudget"] != float64(2048) {
		t.Fatalf("thinkingConfig = %v", thinking)
	}

	if resp.Content != "hello" || resp.ReasoningContent != "thought" {
		t.Fatalf("response text/reasoning = %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "bash" || resp.ToolCalls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.Route != "gemini" || resp.Model != "gemini-2.5-flash" {
		t.Errorf("served route/model = %q/%q", resp.Route, resp.Model)
	}
	u := resp.Usage
	if u == nil || u.PromptTokens != 100 || u.CompletionTokens != 25 || u.TotalTokens != 125 ||
		u.CachedPromptTokens != 40 || u.ReasoningTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
	if p.Name() != "gemini/gemini-2.5-pro" {
		t.Errorf("Name = %q", p.Name())
	}
}

// TestGeminiChatStream covers streamGenerateContent: partial text and thought
// parts, a functionCall part, and usage from the final chunk.
func TestGeminiChatStream(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"pla","thought":true}]}}]}`,
		``,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"n","thought":true}]}}]}`,
		``,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]}}]}`,
		``,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"lo"}]}}]}`,
		``,
		`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"bash","args":{"cmd":"ls"}}}]}}]}`,
		``,
		`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"thoughtsTokenCount":2,"totalTokenCount":12}}`,
		``,
	}, "\n")

	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.WriteString(w, stream)
	}))
	defer srv.Close()

	var reasoning, text []string
	resp, err := NewGeminiProvider("k", srv.URL, "m").Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}},
		StreamCallbacks: &types.StreamCallbacks{
			OnReasoningDelta: func(s string) { reasoning = append(reasoning, s) },
			OnTextDelta:      func(s string) { text = append(text, s) },
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotPath != "/models/m:streamGenerateContent" {
		t.Errorf("stream path = %q", gotPath)
	}
	if got := strings.Join(reasoning, ""); got != "plan" {
		t.Errorf("reasoning deltas = %q", got)
	}
	if got := strings.Join(text, ""); got != "Hello" {
		t.Errorf("text deltas = %q", got)
	}
	if resp.Content != "Hello" || resp.ReasoningContent != "plan" {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	u := resp.Usage
	if u == nil || u.PromptTokens != 7 || u.CompletionTokens != 5 || u.TotalTokens != 12 || u.ReasoningTokens != 2 {
		t.Fatalf("usage = %+v", u)
	}
}

// TestGeminiErrors covers the failure paths.
func TestGeminiErrors(t *testing.T) {
	fastRetries(t)
	status := stubProviderServer(t, http.StatusInternalServerError, "upstream exploded")
	if _, err := NewGeminiProvider("k", status.URL, "m").Chat(context.Background(), types.ChatRequest{}); err == nil ||
		!strings.Contains(err.Error(), "500") {
		t.Errorf("status error = %v", err)
	}

	badJSON := stubProviderServer(t, http.StatusOK, "not json")
	if _, err := NewGeminiProvider("k", badJSON.URL, "m").Chat(context.Background(), types.ChatRequest{}); err == nil ||
		!strings.Contains(err.Error(), "gemini decode") {
		t.Errorf("invalid JSON = %v", err)
	}

	empty := stubProviderServer(t, http.StatusOK, `{"candidates":[]}`)
	if _, err := NewGeminiProvider("k", empty.URL, "m").Chat(context.Background(), types.ChatRequest{}); err == nil ||
		!strings.Contains(err.Error(), "no candidates") {
		t.Errorf("empty candidates = %v", err)
	}
}

// TestGeminiModelPrefix: a model copied as "models/…" is not doubled in the URL.
func TestGeminiModelPrefix(t *testing.T) {
	p := NewGeminiProvider("k", "https://example.invalid", "models/gemini-2.5-pro")
	if got := p.endpoint("models/gemini-2.5-pro", false); got != "https://example.invalid/models/gemini-2.5-pro:generateContent" {
		t.Fatalf("endpoint = %q", got)
	}
}
