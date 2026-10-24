package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// TestAzureRequestShape: an Azure route addresses the deployment path, passes the
// api-version, authenticates with api-key, and otherwise speaks the same OpenAI
// shape — which is why it reuses that provider rather than copying it.
func TestAzureRequestShape(t *testing.T) {
	var gotPath, gotVersion, gotKey, gotAuth string
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotVersion = r.URL.Query().Get("api-version")
		gotKey = r.Header.Get("api-key")
		gotAuth = r.Header.Get("Authorization")
		gotBody = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"hello",`+
			`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}}]}}],`+
			`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
	}))
	defer srv.Close()

	p := NewAzureOpenAIProvider("azure-key", srv.URL, "my-deployment", "2025-01-01-preview")
	p.SetRouteInfo("azure", "", "USD")

	resp, err := p.Chat(context.Background(), types.ChatRequest{
		Messages:  []types.Message{{Role: types.RoleUser, Content: "hi"}},
		MaxTokens: 64,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	if gotPath != "/openai/deployments/my-deployment/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	if gotVersion != "2025-01-01-preview" {
		t.Fatalf("api-version = %q", gotVersion)
	}
	if gotKey != "azure-key" {
		t.Fatalf("api-key = %q", gotKey)
	}
	if gotAuth != "" {
		t.Fatalf("azure request also sent Authorization: %q", gotAuth)
	}
	if gotBody["model"] != "my-deployment" || gotBody["max_tokens"] != float64(64) {
		t.Fatalf("body = %v", gotBody)
	}
	if resp.Content != "hello" || len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Arguments != `{"cmd":"ls"}` {
		t.Fatalf("response = %+v", resp)
	}
	if p.Name() != "azure/my-deployment" {
		t.Errorf("Name = %q", p.Name())
	}

	// A model override is a different deployment: the path has to follow it.
	if _, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}},
		Model:    "other-deployment",
	}); err != nil {
		t.Fatalf("Chat with override: %v", err)
	}
	if gotPath != "/openai/deployments/other-deployment/chat/completions" {
		t.Errorf("override path = %q", gotPath)
	}
	if gotBody["model"] != "other-deployment" {
		t.Errorf("override body model = %v", gotBody["model"])
	}
}

// TestAzureDefaultAPIVersion pins the version a route gets when it names none.
func TestAzureDefaultAPIVersion(t *testing.T) {
	var gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVersion = r.URL.Query().Get("api-version")
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()

	p := NewAzureOpenAIProvider("k", srv.URL, "deploy", "")
	if _, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotVersion != azureAPIVersion {
		t.Fatalf("api-version = %q, want the pinned default %q", gotVersion, azureAPIVersion)
	}
}

// TestAzureKeepsOpenAISemantics: retry, usage and the prompt-cache refusal are
// inherited from the OpenAI-compatible provider; a 429 reaches the retry policy
// exactly as it does on the plain route.
func TestAzureRetriesTransientStatus(t *testing.T) {
	fastRetries(t)
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, "slow down")
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()

	p := NewAzureOpenAIProvider("k", srv.URL, "deploy", "")
	if _, err := p.Chat(context.Background(), types.ChatRequest{
		Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want one 429 and one success", attempts)
	}
}
