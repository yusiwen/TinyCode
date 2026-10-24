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

// TestAnthropicChatBatch covers the native Messages shape: the endpoint and
// headers, the system/messages split, tool_use/tool_result mapping, thinking
// replay with its signature, and the four usage lanes.
func TestAnthropicChatBatch(t *testing.T) {
	var gotPath, gotKey, gotVersion string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"content":[`+
			`{"type":"thinking","thinking":"reasoned","signature":"sig-1"},`+
			`{"type":"text","text":"hello"},`+
			`{"type":"tool_use","id":"tool_1","name":"bash","input":{"cmd":"ls"}}],`+
			`"stop_reason":"tool_use",`+
			`"usage":{"input_tokens":100,"output_tokens":20,`+
			`"cache_read_input_tokens":900,"cache_creation_input_tokens":50}}`)
	}))
	defer srv.Close()

	p := NewAnthropicProvider("sk-ant", srv.URL, "claude-sonnet-4")
	p.SetRouteInfo("claude", "", "USD")
	p.SetThinkingBudget(2048)

	resp, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{
			{Role: types.RoleSystem, Content: "sys"},
			{Role: types.RoleUser, Content: "hi"},
			{Role: types.RoleAssistant, Content: "answer", ReasoningContent: "previous", ReasoningSignature: "sig-prev",
				ToolCalls: []types.ToolCall{{ID: "tool_9", Name: "read_file", Arguments: `{"path":"a"}`}}},
			{Role: types.RoleTool, Name: "read_file", ToolCallID: "tool_9", Content: "file body"},
		},
		Tools:     []types.ToolDef{{Name: "bash", Description: "run", Parameters: map[string]any{"type": "object"}}},
		MaxTokens: 128,
		Model:     "override-model",
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotPath != "/v1/messages" {
		t.Fatalf("request path = %q", gotPath)
	}
	if gotKey != "sk-ant" || gotVersion != anthropicVersion {
		t.Fatalf("auth headers = %q / %q", gotKey, gotVersion)
	}
	if gotBody["model"] != "override-model" || gotBody["max_tokens"] != float64(128) {
		t.Fatalf("model/max_tokens = %v / %v", gotBody["model"], gotBody["max_tokens"])
	}
	thinking, _ := gotBody["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(2048) {
		t.Fatalf("thinking = %v", gotBody["thinking"])
	}

	system, _ := gotBody["system"].([]any)
	if len(system) != 1 {
		t.Fatalf("system = %v", gotBody["system"])
	}
	if block := system[0].(map[string]any); block["text"] != "sys" || block["type"] != "text" {
		t.Fatalf("system block = %v", block)
	}
	if _, ok := system[0].(map[string]any)["cache_control"]; ok {
		t.Error("a route without an explicit policy sent cache_control")
	}

	msgs, _ := gotBody["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("sent %d messages, want 3 (system is top-level)", len(msgs))
	}
	assistant, _ := msgs[1].(map[string]any)
	if assistant["role"] != "assistant" {
		t.Fatalf("second message = %v", assistant)
	}
	blocks, _ := assistant["content"].([]any)
	if len(blocks) != 3 {
		t.Fatalf("assistant blocks = %v", blocks)
	}
	if b := blocks[0].(map[string]any); b["type"] != "thinking" || b["signature"] != "sig-prev" || b["thinking"] != "previous" {
		t.Fatalf("thinking replay = %v", b)
	}
	if b := blocks[2].(map[string]any); b["type"] != "tool_use" || b["id"] != "tool_9" || b["name"] != "read_file" {
		t.Fatalf("tool_use = %v", b)
	}
	toolResult, _ := msgs[2].(map[string]any)
	results, _ := toolResult["content"].([]any)
	if len(results) != 1 {
		t.Fatalf("tool results = %v", toolResult)
	}
	if b := results[0].(map[string]any); b["type"] != "tool_result" || b["tool_use_id"] != "tool_9" || b["content"] != "file body" {
		t.Fatalf("tool_result = %v", b)
	}

	if resp.Content != "hello" || resp.ReasoningContent != "reasoned" || resp.ReasoningSignature != "sig-1" {
		t.Fatalf("response text/reasoning = %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "tool_1" || resp.ToolCalls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if resp.Route != "claude" || resp.Model != "override-model" {
		t.Errorf("served route/model = %q/%q", resp.Route, resp.Model)
	}
	u := resp.Usage
	if u == nil {
		t.Fatal("no usage parsed")
	}
	if u.PromptTokens != 1050 || u.CompletionTokens != 20 || u.TotalTokens != 1070 {
		t.Errorf("usage totals = %+v, want prompt 1050 (input+cache), completion 20", u)
	}
	if u.CachedPromptTokens != 900 || u.CacheWriteTokens != 50 {
		t.Errorf("usage lanes = %+v", u)
	}
	if p.Name() != "anthropic/claude-sonnet-4" {
		t.Errorf("Name = %q", p.Name())
	}
}

// TestAnthropicEmptyToolResultKeepsTheRequiredField: a command that printed
// nothing is still a tool result, and the API requires the content field.
func TestAnthropicEmptyToolResultKeepsTheRequiredField(t *testing.T) {
	_, messages := anthropicMessages([]types.Message{
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{ID: "t1", Name: "bash", Arguments: `{}`}}},
		{Role: types.RoleTool, Name: "bash", ToolCallID: "t1", Content: ""},
	})
	raw, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"content":""`) {
		t.Fatalf("empty tool result lost its content field: %s", raw)
	}
}

// TestAnthropicPromptCache: explicit places one breakpoint on the last leading
// system block and a one-hour TTL asks for the vendor's beta header.
func TestAnthropicPromptCache(t *testing.T) {
	var gotBeta string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBeta = r.Header.Get("anthropic-beta")
		gotBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	p := NewAnthropicProvider("k", srv.URL, "m")
	p.SetPromptCache(PromptCachePolicy{Mode: PromptCacheExplicit, TTL: "1h"})
	if _, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{
			{Role: types.RoleSystem, Content: "first"},
			{Role: types.RoleSystem, Content: "second"},
			{Role: types.RoleUser, Content: "hi"},
		},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotBeta != anthropicCacheTTLBeta {
		t.Errorf("anthropic-beta = %q, want %q", gotBeta, anthropicCacheTTLBeta)
	}
	system, _ := gotBody["system"].([]any)
	last := system[len(system)-1].(map[string]any)
	cc, _ := last["cache_control"].(map[string]any)
	if cc["type"] != "ephemeral" || cc["ttl"] != "1h" {
		t.Fatalf("cache_control = %v", last["cache_control"])
	}
	if _, ok := system[0].(map[string]any)["cache_control"]; ok {
		t.Error("the first system block was marked instead of the last")
	}

	// The default five-minute lifetime sends no beta header.
	p5 := NewAnthropicProvider("k", srv.URL, "m")
	p5.SetPromptCache(PromptCachePolicy{Mode: PromptCacheExplicit})
	if _, err := p5.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{{Role: types.RoleSystem, Content: "s"}, {Role: types.RoleUser, Content: "u"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotBeta != "" {
		t.Errorf("a 5m cache sent the 1h beta header: %q", gotBeta)
	}
}

// TestAnthropicChatStream covers the SSE event sequence: thinking and signature
// deltas, text, a tool call assembled from input_json_delta, and usage split
// between message_start and message_delta.
func TestAnthropicChatStream(t *testing.T) {
	stream := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":90,"cache_creation_input_tokens":5,"output_tokens":1}}}`,
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan"}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}`,
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":0}`,
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hel"}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"lo"}}`,
		"event: content_block_start",
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tool_1","name":"bash"}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":"}}`,
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"ls\"}"}}`,
		"event: content_block_stop",
		`data: {"type":"content_block_stop","index":2}`,
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["stream"] != true {
			t.Error("the request did not enable streaming")
		}
		io.WriteString(w, stream)
	}))
	defer srv.Close()

	var reasoning, text []string
	resp, err := NewAnthropicProvider("k", srv.URL, "m").Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}},
		StreamCallbacks: &types.StreamCallbacks{
			OnReasoningDelta: func(s string) { reasoning = append(reasoning, s) },
			OnTextDelta:      func(s string) { text = append(text, s) },
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if got := strings.Join(reasoning, ""); got != "plan" {
		t.Errorf("reasoning deltas = %q", got)
	}
	if got := strings.Join(text, ""); got != "Hello" {
		t.Errorf("text deltas = %q", got)
	}
	if resp.Content != "Hello" || resp.ReasoningContent != "plan" || resp.ReasoningSignature != "sig-1" {
		t.Fatalf("response = %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "tool_1" ||
		resp.ToolCalls[0].Name != "bash" || resp.ToolCalls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	u := resp.Usage
	if u == nil || u.PromptTokens != 105 || u.CompletionTokens != 7 || u.TotalTokens != 112 ||
		u.CachedPromptTokens != 90 || u.CacheWriteTokens != 5 {
		t.Fatalf("streamed usage = %+v", u)
	}
}

// TestAnthropicErrors covers the failure paths that must surface an error rather
// than a partial answer.
func TestAnthropicErrors(t *testing.T) {
	fastRetries(t)
	status := stubProviderServer(t, http.StatusInternalServerError, "upstream exploded")
	if _, err := NewAnthropicProvider("k", status.URL, "m").Chat(context.Background(), types.ChatRequest{}); err == nil ||
		!strings.Contains(err.Error(), "500") {
		t.Errorf("status error = %v, want it to mention 500", err)
	}

	badJSON := stubProviderServer(t, http.StatusOK, "not json")
	if _, err := NewAnthropicProvider("k", badJSON.URL, "m").Chat(context.Background(), types.ChatRequest{}); err == nil ||
		!strings.Contains(err.Error(), "anthropic decode") {
		t.Errorf("invalid JSON = %v", err)
	}

	empty := stubProviderServer(t, http.StatusOK, `{"content":[],"usage":{"input_tokens":1}}`)
	if _, err := NewAnthropicProvider("k", empty.URL, "m").Chat(context.Background(), types.ChatRequest{}); err == nil ||
		!strings.Contains(err.Error(), "no content blocks") {
		t.Errorf("empty content = %v", err)
	}

	// A 200 carrying an error event is a failure, not an empty answer.
	errStream := strings.Join([]string{
		"event: error",
		`data: {"type":"error","error":{"type":"overloaded_error","message":"try later"}}`,
		"",
	}, "\n")
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, errStream)
	}))
	defer errSrv.Close()
	if _, err := NewAnthropicProvider("k", errSrv.URL, "m").Chat(context.Background(), types.ChatRequest{
		StreamCallbacks: &types.StreamCallbacks{OnTextDelta: func(string) {}},
	}); err == nil || !strings.Contains(err.Error(), "overloaded_error") {
		t.Errorf("error event = %v", err)
	}
}

// TestAnthropicBaseURLWithV1: a base copied from the vendor documentation may
// already end in /v1; it must not become /v1/v1/messages.
func TestAnthropicBaseURLWithV1(t *testing.T) {
	p := NewAnthropicProvider("k", "https://api.anthropic.com/v1/", "m")
	if got := p.endpoint(); got != "https://api.anthropic.com/v1/messages" {
		t.Fatalf("endpoint = %q", got)
	}
}
