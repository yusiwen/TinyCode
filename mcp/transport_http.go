package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

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
		client:  &http.Client{},
		nextID:  1,
	}
}

func (c *HTTPClient) buildMessage(method string, params json.RawMessage) []byte {
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
	return body
}

func (c *HTTPClient) sendMessage(body []byte) (json.RawMessage, error) {
	req, err := http.NewRequest("POST", c.baseURL, bytes.NewReader(body))
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

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var rpcResp jsonrpcMessage
	if err := json.Unmarshal(raw, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	return rpcResp.Result, nil
}

func (c *HTTPClient) send(method string, params json.RawMessage) (json.RawMessage, error) {
	body := c.buildMessage(method, params)
	return c.sendMessage(body)
}

func (c *HTTPClient) Initialize() (*ServerInfo, error) {
	params := map[string]any{
		"protocolVersion": "2025-03-26",
		"clientInfo": map[string]string{
			"name":    "tinycode",
			"version": "0.0.3",
		},
	}
	rawParams, _ := json.Marshal(params)
	raw, err := c.send("initialize", rawParams)
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

func (c *HTTPClient) ListTools() ([]Tool, error) {
	raw, err := c.send("tools/list", nil)
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

func (c *HTTPClient) CallTool(name string, args map[string]any) (*ToolResult, error) {
	params := map[string]any{
		"name":      name,
		"arguments": args,
	}
	rawParams, _ := json.Marshal(params)
	raw, err := c.send("tools/call", rawParams)
	if err != nil {
		return nil, fmt.Errorf("tools/call %q: %w", name, err)
	}

	var result ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse tools/call result: %w", err)
	}
	return &result, nil
}

func (c *HTTPClient) Tools() []Tool {
	return c.tools
}

func (c *HTTPClient) Info() *ServerInfo {
	return c.serverInfo
}
