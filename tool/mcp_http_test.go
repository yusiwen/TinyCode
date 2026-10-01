package tool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/config"
)

// mcpHTTPServer is a minimal MCP server over HTTP: it answers `initialize` and
// `tools/list` like a real one, and records the headers it was sent.
func mcpHTTPServer(t *testing.T, sawHeader *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sawHeader != nil {
			*sawHeader = r.Header.Get("X-Api-Key")
		}
		var req struct {
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"http-mock","version":"1"}}}`)
		case "tools/list":
			io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{"tools":[
				{"name":"echo","description":"Echo input","inputSchema":{"type":"object"}}]}}`)
		default:
			io.WriteString(w, `{"jsonrpc":"2.0","id":3,"result":{}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestConnectMCPHTTP covers the HTTP transport path end to end: the handshake,
// tool discovery, the configured headers, and the client it returns. The stdio
// path is covered by mcp_test.go; this one had never been executed (issue #4).
func TestConnectMCPHTTP(t *testing.T) {
	var sawHeader string
	srv := mcpHTTPServer(t, &sawHeader)

	cfg := &config.MCPServerConfig{
		Name:      "httpx",
		Transport: "http",
		URL:       srv.URL,
		Headers:   map[string]string{"X-Api-Key": "secret"},
	}

	client, err := connectMCPHTTP(context.Background(), cfg, 5*time.Second)
	if err != nil {
		t.Fatalf("connectMCPHTTP: %v", err)
	}
	defer client.Client.Close()

	if client.ServerName != "httpx" {
		t.Errorf("server name = %q, want the configured one", client.ServerName)
	}
	if len(client.Tools) != 1 || client.Tools[0].Name != "echo" {
		t.Errorf("tools = %+v, want the one the server listed", client.Tools)
	}
	if sawHeader != "secret" {
		t.Errorf("X-Api-Key = %q, want the configured header to be sent", sawHeader)
	}
}

// TestConnectMCPHTTPFailures covers the two refusal shapes: a server that answers
// with an error status, and an endpoint the SSRF policy refuses before any request
// is made.
func TestConnectMCPHTTPFailures(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "server exploded", http.StatusInternalServerError)
	}))
	defer failing.Close()

	_, err := connectMCPHTTP(context.Background(), &config.MCPServerConfig{
		Name: "broken", Transport: "http", URL: failing.URL,
	}, 5*time.Second)
	if err == nil {
		t.Fatal("a 500 during the handshake must fail the connection")
	}
	if !strings.Contains(err.Error(), "initialize") || !strings.Contains(err.Error(), "500") {
		t.Errorf("error = %v, want it to name the handshake and the status", err)
	}

	// A metadata address is refused by checkMCPURL, so no request is attempted.
	_, err = connectMCPHTTP(context.Background(), &config.MCPServerConfig{
		Name: "metadata", Transport: "http", URL: "http://169.254.169.254/mcp",
	}, 5*time.Second)
	if err == nil {
		t.Fatal("the SSRF policy must refuse a link-local endpoint")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error = %v, want the SSRF refusal", err)
	}

	// A non-http scheme is refused as well, before any transport is built.
	if err := checkMCPURL("file:///etc/passwd"); err == nil {
		t.Error("checkMCPURL accepted a non-http scheme")
	}
	// Unspecified addresses bind every interface and must not be treated as a
	// remote endpoint.
	if err := checkMCPURL("http://0.0.0.0:8080/"); err == nil {
		t.Error("checkMCPURL accepted the unspecified address")
	}
	// A loopback endpoint is the documented development exception.
	if err := checkMCPURL("http://127.0.0.1:3000/mcp"); err != nil {
		t.Errorf("checkMCPURL refused loopback: %v", err)
	}
	if err := checkMCPURL("http://localhost:3000/mcp"); err != nil {
		t.Errorf("checkMCPURL refused localhost: %v", err)
	}
}
