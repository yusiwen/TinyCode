package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHTTPInitialize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"http-test","version":"1.0.0"}}}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, nil)
	info, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if info.Name != "http-test" {
		t.Errorf("expected http-test, got %q", info.Name)
	}
}

func TestHTTPListTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg jsonrpcMessage
		json.NewDecoder(r.Body).Decode(&msg)
		switch msg.Method {
		case "initialize":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"t","version":"1"}}}`))
		case "tools/list":
			w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[
				{"name":"greet","description":"Say hello","inputSchema":{"type":"object"}}
			]}}`))
		}
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, nil)
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "greet" {
		t.Errorf("expected 'greet', got %+v", tools)
	}
}

func TestHTTPCallTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg jsonrpcMessage
		json.NewDecoder(r.Body).Decode(&msg)
		switch msg.Method {
		case "initialize":
			w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"t","version":"1"}}}`))
		case "tools/list":
			w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"tools":[]}}`))
		case "tools/call":
			w.Write([]byte(`{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"hello"}]}}`))
		}
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, nil)
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	result, err := client.CallTool(context.Background(), "greet", map[string]any{"name": "world"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(result.Content) != 1 || result.Content[0].Text != "hello" {
		t.Errorf("expected 'hello', got %+v", result.Content)
	}
}

func TestHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, nil)
	_, err := client.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("expected 'HTTP 500', got: %v", err)
	}
}

func TestHTTPJSONError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"Method not found"}}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, nil)
	_, err := client.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Method not found") {
		t.Errorf("expected 'Method not found', got: %v", err)
	}
}

func TestHTTPCustomHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test123" {
			t.Errorf("expected Authorization header, got %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"t","version":"1"}}}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, map[string]string{
		"Authorization": "Bearer test123",
	})
	_, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}

// TestHTTPClientHasDefaultTimeout guards against a client with no timeout,
// which would hang forever on a blackholed endpoint.
func TestHTTPClientHasDefaultTimeout(t *testing.T) {
	client := NewHTTPClient("http://127.0.0.1:1/mcp", nil)
	if client.client.Timeout <= 0 {
		t.Fatalf("expected a non-zero HTTP client timeout, got %v", client.client.Timeout)
	}
}

// TestHTTPRequestRespectsContext ensures the caller's context reaches the
// HTTP request instead of being ignored.
func TestHTTPRequestRespectsContext(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)

	client := NewHTTPClient(server.URL, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.Initialize(ctx)
	if err == nil {
		t.Fatal("expected an error from the cancelled context, got nil")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("request did not respect the context deadline; took %v", elapsed)
	}
}

// TestHTTPResponseIDMismatchRejected ensures a response for a different
// request is not accepted as ours.
func TestHTTPResponseIDMismatchRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":99,"result":{"serverInfo":{"name":"wrong","version":"1"}}}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, nil)
	if _, err := client.Initialize(context.Background()); err == nil {
		t.Fatal("expected error for mismatched response id, got nil")
	}
}

// TestHTTPTransportRefusesRedirectToMetadata proves the MCP HTTP transport uses
// the shared SSRF-hardened client: a compromised endpoint cannot redirect the
// agent to the cloud metadata service.
func TestHTTPTransportRefusesRedirectToMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, nil)
	if _, err := client.Initialize(context.Background()); err == nil {
		t.Fatal("expected a 302 to the cloud metadata endpoint to be refused")
	} else if !strings.Contains(err.Error(), "redirect blocked") {
		t.Fatalf("expected a redirect-blocked error, got: %v", err)
	}
}

// TestHTTPTransportReachesLoopbackServer documents that the loopback allowance
// keeps a deliberately configured local MCP server reachable through the
// SSRF-hardened transport.
func TestHTTPTransportReachesLoopbackServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"local","version":"1.0.0"}}}`))
	}))
	defer server.Close()

	client := NewHTTPClient(server.URL, nil)
	info, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize against a loopback server: %v", err)
	}
	if info.Name != "local" {
		t.Fatalf("expected server name %q, got %q", "local", info.Name)
	}
}

// TestHTTPTransportLoopbackScopedToConfiguredEndpoint pins the security
// boundary: loopback is reachable only when the configured endpoint is itself
// loopback, so a public MCP endpoint cannot be redirected to a local service.
func TestHTTPTransportLoopbackScopedToConfiguredEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	local, ok := NewHTTPClient("http://127.0.0.1:9000/mcp", nil).client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected the MCP transport to use *http.Transport")
	}
	// Validation must pass for the configured loopback endpoint; the dial then
	// fails only because nothing listens on that port.
	if _, err := local.DialContext(ctx, "tcp", "127.0.0.1:1"); err == nil {
		t.Log("unexpectedly connected to 127.0.0.1:1")
	} else if strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("a configured loopback endpoint must stay reachable, got: %v", err)
	}

	remote, ok := NewHTTPClient("https://mcp.example.com/mcp", nil).client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected the MCP transport to use *http.Transport")
	}
	if _, err := remote.DialContext(ctx, "tcp", "127.0.0.1:1"); err == nil {
		t.Fatal("a public MCP endpoint must not be able to dial loopback")
	} else if !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("expected an SSRF block for a public endpoint, got: %v", err)
	}
}
