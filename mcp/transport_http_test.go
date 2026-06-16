package mcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
	info, err := client.Initialize()
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
	if _, err := client.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := client.ListTools()
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
	if _, err := client.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ListTools(); err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	result, err := client.CallTool("greet", map[string]any{"name": "world"})
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
	_, err := client.Initialize()
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
	_, err := client.Initialize()
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
	_, err := client.Initialize()
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
}
