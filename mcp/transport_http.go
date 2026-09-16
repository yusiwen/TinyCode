package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/yusiwen/tinycode/internal/netsafe"
)

// httpRequestTimeout bounds a single HTTP MCP request when the caller's
// context carries no earlier deadline. Without it a blackholed endpoint would
// hang forever.
const httpRequestTimeout = 30 * time.Second

// HTTPClient is an MCP client that communicates via HTTP POST.
type HTTPClient struct {
	baseURL string
	headers map[string]string
	client  *http.Client

	mu     sync.Mutex
	nextID int

	serverInfo *ServerInfo
	tools      []Tool
}

// NewHTTPClient creates an MCP client using HTTP transport.
func NewHTTPClient(baseURL string, headers map[string]string) *HTTPClient {
	return &HTTPClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		headers: headers,
		client:  newSSRFProtectedClient(baseURL),
		nextID:  1,
	}
}

// newSSRFProtectedClient builds the transport client with the shared SSRF
// policy: the configured host is resolved once and the validated IP is pinned,
// and every redirect target is re-validated.
//
// Loopback stays reachable only when the configured endpoint is itself
// loopback. checkMCPURL deliberately whitelists localhost MCP servers for
// development, so the client must be able to reach them; a public endpoint,
// however, still cannot be redirected to a local service.
func newSSRFProtectedClient(baseURL string) *http.Client {
	var opts []netsafe.Option
	if u, err := url.Parse(baseURL); err == nil && netsafe.IsLoopbackHost(u.Hostname()) {
		// Exempt exactly the configured authority: a local endpoint may not
		// redirect this client to some other loopback service.
		port := u.Port()
		if port == "" {
			if u.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		opts = append(opts, netsafe.AllowAuthority(net.JoinHostPort(u.Hostname(), port)))
	}
	return netsafe.NewClient(httpRequestTimeout, true, opts...)
}

// Close releases idle HTTP connections held by the client.
func (c *HTTPClient) Close() error {
	c.client.CloseIdleConnections()
	return nil
}

func (c *HTTPClient) buildMessage(method string, params json.RawMessage) ([]byte, int) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()

	msg := jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	body, _ := json.Marshal(msg)
	return body, id
}

func (c *HTTPClient) sendMessage(ctx context.Context, id int, body []byte) (json.RawMessage, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxMessageSize))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var rpcResp jsonrpcMessage
	if err := json.Unmarshal(raw, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	// A response carrying a different request id belongs to another call and
	// must not be returned as ours. An omitted id is tolerated because some
	// servers reply without one.
	if rpcResp.ID != 0 && rpcResp.ID != id {
		return nil, fmt.Errorf("response id %d does not match request id %d", rpcResp.ID, id)
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	return rpcResp.Result, nil
}

func (c *HTTPClient) send(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	body, id := c.buildMessage(method, params)
	return c.sendMessage(ctx, id, body)
}

func (c *HTTPClient) Initialize(ctx context.Context) (*ServerInfo, error) {
	params := map[string]any{
		"protocolVersion": "2025-03-26",
		"clientInfo": map[string]string{
			"name":    "tinycode",
			"version": "0.0.4",
		},
	}
	rawParams, _ := json.Marshal(params)
	raw, err := c.send(ctx, "initialize", rawParams)
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}

	var result struct {
		ServerInfo ServerInfo `json:"serverInfo"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse initialize result: %w", err)
	}
	c.serverInfo = &result.ServerInfo
	return &result.ServerInfo, nil
}

func (c *HTTPClient) ListTools(ctx context.Context) ([]Tool, error) {
	raw, err := c.send(ctx, "tools/list", nil)
	if err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}

	var result struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse tools/list result: %w", err)
	}
	c.tools = result.Tools
	return result.Tools, nil
}

func (c *HTTPClient) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	params := map[string]any{
		"name":      name,
		"arguments": args,
	}
	rawParams, _ := json.Marshal(params)
	raw, err := c.send(ctx, "tools/call", rawParams)
	if err != nil {
		return nil, fmt.Errorf("tools/call %q: %w", name, err)
	}

	var result ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse tools/call result: %w", err)
	}
	return &result, nil
}

func (c *HTTPClient) ListResources(ctx context.Context) ([]Resource, error) {
	raw, err := c.send(ctx, "resources/list", nil)
	if err != nil {
		return nil, fmt.Errorf("resources/list: %w", err)
	}
	var result struct {
		Resources []Resource `json:"resources"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse resources/list result: %w", err)
	}
	return result.Resources, nil
}

func (c *HTTPClient) ReadResource(ctx context.Context, uri string) (*ResourceResult, error) {
	params, _ := json.Marshal(map[string]any{"uri": uri})
	raw, err := c.send(ctx, "resources/read", params)
	if err != nil {
		return nil, fmt.Errorf("resources/read: %w", err)
	}
	var result ResourceResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse resources/read result: %w", err)
	}
	return &result, nil
}

func (c *HTTPClient) Tools() []Tool {
	return c.tools
}

func (c *HTTPClient) Info() *ServerInfo {
	return c.serverInfo
}
