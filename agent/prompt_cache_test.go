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

// TestResolvePromptCache pins the configuration grammar: the accepted modes, the
// two TTLs, and the combinations that must be refused rather than read as off.
func TestResolvePromptCache(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		ttl     string
		want    PromptCachePolicy
		wantErr string
	}{
		{name: "absent", mode: "", want: PromptCachePolicy{Mode: PromptCacheOff}},
		{name: "off", mode: "off", want: PromptCachePolicy{Mode: PromptCacheOff}},
		{name: "auto", mode: "auto", want: PromptCachePolicy{Mode: PromptCacheAuto}},
		{name: "explicit", mode: "explicit", want: PromptCachePolicy{Mode: PromptCacheExplicit}},
		{name: "explicit with 5m", mode: "explicit", ttl: "5m", want: PromptCachePolicy{Mode: PromptCacheExplicit}},
		{name: "explicit with 1h", mode: "explicit", ttl: "1h", want: PromptCachePolicy{Mode: PromptCacheExplicit, TTL: "1h"}},
		{name: "case and space", mode: " Explicit ", ttl: "1H", want: PromptCachePolicy{Mode: PromptCacheExplicit, TTL: "1h"}},
		{name: "unknown mode", mode: "always", wantErr: `unknown mode "always"`},
		{name: "unknown ttl", mode: "explicit", ttl: "24h", wantErr: `unknown ttl "24h"`},
		{name: "ttl with auto", mode: "auto", ttl: "5m", wantErr: `ttl "5m" needs mode "explicit"`},
		{name: "ttl with off", mode: "off", ttl: "1h", wantErr: `ttl "1h" needs mode "explicit"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolvePromptCache(tc.mode, tc.ttl)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ResolvePromptCache(%q, %q) error = %v, want %q", tc.mode, tc.ttl, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolvePromptCache(%q, %q): %v", tc.mode, tc.ttl, err)
			}
			if got != tc.want {
				t.Errorf("ResolvePromptCache(%q, %q) = %+v, want %+v", tc.mode, tc.ttl, got, tc.want)
			}
		})
	}
}

// cacheBody runs one batch Chat against a stub and returns the request body it
// received. The response parses as an ordinary answer, so the body under test is
// the only thing being asserted.
func cacheBody(t *testing.T, policy PromptCachePolicy, req types.ChatRequest) map[string]any {
	t.Helper()
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()

	p := NewOpenAIProvider("sk-test", srv.URL, "test-model")
	p.SetPromptCache(policy)
	if _, err := p.Chat(context.Background(), req); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if body == nil {
		t.Fatal("no request reached the stub")
	}
	return body
}

func cacheTestRequest() types.ChatRequest {
	return types.ChatRequest{
		Messages: []types.Message{
			{Role: types.RoleSystem, Content: "you are a coding agent"},
			{Role: types.RoleUser, Content: "first prompt"},
		},
		Tools: []types.ToolDef{
			{Name: "bash", Description: "run a command", Parameters: map[string]any{"type": "object"}},
			{Name: "read_file", Description: "read a file", Parameters: map[string]any{"type": "object"}},
		},
		MaxTokens: 128,
	}
}

func messageAt(t *testing.T, body map[string]any, i int) map[string]any {
	t.Helper()
	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) <= i {
		t.Fatalf("messages = %v", body["messages"])
	}
	m, ok := msgs[i].(map[string]any)
	if !ok {
		t.Fatalf("message %d = %T", i, msgs[i])
	}
	return m
}

// TestPromptCacheOffSendsNothing pins the default: a route built without a policy
// keeps the old body, string content included.
func TestPromptCacheOffSendsNothing(t *testing.T) {
	body := cacheBody(t, PromptCachePolicy{}, cacheTestRequest())
	if _, ok := body["prompt_cache_key"]; ok {
		t.Errorf("off sent prompt_cache_key = %v", body["prompt_cache_key"])
	}
	content := messageAt(t, body, 0)["content"]
	if _, ok := content.(string); !ok {
		t.Errorf("system content = %T, want a plain string", content)
	}
}

// TestPromptCacheAutoSendsAStableKey pins the routing key: present, derived from
// the prefix rather than the conversation, and changed by a prefix change.
func TestPromptCacheAutoSendsAStableKey(t *testing.T) {
	first := cacheBody(t, PromptCachePolicy{Mode: PromptCacheAuto}, cacheTestRequest())
	key, ok := first["prompt_cache_key"].(string)
	if !ok || key == "" {
		t.Fatalf("prompt_cache_key = %v, want a non-empty string", first["prompt_cache_key"])
	}
	if _, ok := messageAt(t, first, 0)["content"].(string); !ok {
		t.Error("auto mode rewrote the system content; only explicit places a breakpoint")
	}

	// A different user prompt is the same prefix.
	second := cacheTestRequest()
	second.Messages[1].Content = "a completely different prompt"
	other := cacheBody(t, PromptCachePolicy{Mode: PromptCacheAuto}, second)
	if other["prompt_cache_key"] != key {
		t.Errorf("key changed with the user prompt: %v → %v", key, other["prompt_cache_key"])
	}

	// A changed tool schema is not.
	third := cacheTestRequest()
	third.Tools[1].Description = "read a file from disk"
	changed := cacheBody(t, PromptCachePolicy{Mode: PromptCacheAuto}, third)
	if changed["prompt_cache_key"] == key {
		t.Error("key did not change when a tool description did")
	}

	// Nor is a changed system prompt.
	fourth := cacheTestRequest()
	fourth.Messages[0].Content = "you are a coding agent."
	changed = cacheBody(t, PromptCachePolicy{Mode: PromptCacheAuto}, fourth)
	if changed["prompt_cache_key"] == key {
		t.Error("key did not change when the system prompt did")
	}
}

// TestPromptCacheExplicitMarksTheStablePrefix pins the wire shape Anthropic-style
// routes need: one text block carrying cache_control, and no other message
// restructured.
func TestPromptCacheExplicitMarksTheStablePrefix(t *testing.T) {
	body := cacheBody(t, PromptCachePolicy{Mode: PromptCacheExplicit}, cacheTestRequest())

	if _, ok := body["prompt_cache_key"].(string); !ok {
		t.Error("explicit mode did not send the routing key")
	}

	blocks, ok := messageAt(t, body, 0)["content"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("system content = %v, want one text block", messageAt(t, body, 0)["content"])
	}
	block, ok := blocks[0].(map[string]any)
	if !ok {
		t.Fatalf("block = %T", blocks[0])
	}
	if block["type"] != "text" || block["text"] != "you are a coding agent" {
		t.Errorf("block = %v, want the unchanged system text", block)
	}
	control, ok := block["cache_control"].(map[string]any)
	if !ok || control["type"] != "ephemeral" {
		t.Fatalf("cache_control = %v, want ephemeral", block["cache_control"])
	}
	if _, ok := control["ttl"]; ok {
		t.Errorf("default ttl was sent explicitly: %v", control)
	}

	// The user message and the tools keep their wire form.
	if content, ok := messageAt(t, body, 1)["content"].(string); !ok || content != "first prompt" {
		t.Errorf("user content = %v, want the plain string", messageAt(t, body, 1)["content"])
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("tools = %v, want the two definitions", body["tools"])
	}
}

// TestPromptCacheExplicitHonoursTheOneHourTTL pins the only TTL that has to reach
// the wire, and that it is absent unless configured.
func TestPromptCacheExplicitHonoursTheOneHourTTL(t *testing.T) {
	body := cacheBody(t, PromptCachePolicy{Mode: PromptCacheExplicit, TTL: "1h"}, cacheTestRequest())
	blocks := messageAt(t, body, 0)["content"].([]any)
	control := blocks[0].(map[string]any)["cache_control"].(map[string]any)
	if control["ttl"] != "1h" {
		t.Errorf("ttl = %v, want 1h", control["ttl"])
	}
}

// TestPromptCacheExplicitWithoutASystemMessage pins the fallback: a request with
// no stable block gets the key but no invented breakpoint.
func TestPromptCacheExplicitWithoutASystemMessage(t *testing.T) {
	req := cacheTestRequest()
	req.Messages = req.Messages[1:]
	body := cacheBody(t, PromptCachePolicy{Mode: PromptCacheExplicit}, req)

	if _, ok := body["prompt_cache_key"].(string); !ok {
		t.Error("explicit mode dropped the routing key when there was no system message")
	}
	if content, ok := messageAt(t, body, 0)["content"].(string); !ok || content != "first prompt" {
		t.Errorf("user content = %v, want the untouched string", messageAt(t, body, 0)["content"])
	}
}
