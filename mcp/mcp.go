package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	initTimeout = 5 * time.Second
)

// Tool represents a tool exposed by an MCP server.
type Tool struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// ToolResult represents the result of calling an MCP tool.
type ToolResult struct {
	Content []ToolResultContent `json:"content"`
	IsError bool                `json:"isError,omitempty"`
}

// ToolResultContent carries the actual result data.
type ToolResultContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Resource represents a resource exposed by an MCP server.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

// ResourceResult carries the content of a read resource.
type ResourceResult struct {
	Contents []ResourceContent `json:"contents"`
}

// ResourceContent carries the actual content data.
type ResourceContent struct {
	URI     string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text    string `json:"text"`
	Blob    string `json:"blob,omitempty"`
}

// ServerInfo holds the identification from an MCP server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// jsonrpcMessage represents a JSON-RPC 2.0 message.
type jsonrpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *jsonrpcError) Error() string {
	return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message)
}

// MCPClient is the interface all MCP transports implement.
type MCPClient interface {
	Initialize() (*ServerInfo, error)
	ListTools() ([]Tool, error)
	CallTool(name string, args map[string]any) (*ToolResult, error)
	ListResources() ([]Resource, error)
	ReadResource(uri string) (*ResourceResult, error)
	Tools() []Tool
	Info() *ServerInfo
}

// Client manages a JSON-RPC 2.0 connection to an MCP server over stdio.
type Client struct {
	stdin  io.Writer
	stdout io.Reader
	stderr io.Reader

	mu       sync.Mutex
	nextID   int

	serverInfo *ServerInfo
	tools      []Tool
}

// NewClient creates an MCP client using the given stdio/stderr streams.
func NewClient(stdin io.Writer, stdout io.Reader, stderr io.Reader) *Client {
	return &Client{
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
		nextID: 1,
	}
}

// Initialize sends the initialize request and waits for a response.
func (c *Client) Initialize() (*ServerInfo, error) {
	params := map[string]any{
		"protocolVersion": "2025-03-26",
		"clientInfo": map[string]string{
			"name":    "tinycode",
			"version": "0.0.4",
		},
	}
	raw, err := c.send("initialize", params)
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

// ListTools retrieves the list of tools from the MCP server.
func (c *Client) ListTools() ([]Tool, error) {
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

// CallTool invokes a tool by name with the given arguments.
func (c *Client) CallTool(name string, args map[string]any) (*ToolResult, error) {
	params := map[string]any{
		"name":      name,
		"arguments": args,
	}
	raw, err := c.send("tools/call", params)
	if err != nil {
		return nil, fmt.Errorf("tools/call %q: %w", name, err)
	}

	var result ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse tools/call result: %w", err)
	}
	return &result, nil
}

// ListResources retrieves the list of resources from the MCP server.
func (c *Client) ListResources() ([]Resource, error) {
	raw, err := c.send("resources/list", nil)
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

// ReadResource reads the content of a resource identified by its URI.
func (c *Client) ReadResource(uri string) (*ResourceResult, error) {
	params := map[string]any{
		"uri": uri,
	}
	raw, err := c.send("resources/read", params)
	if err != nil {
		return nil, fmt.Errorf("resources/read %q: %w", uri, err)
	}

	var result ResourceResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("parse resources/read result: %w", err)
	}
	return &result, nil
}

// Tools returns the cached tool list from the last ListTools call.
func (c *Client) Tools() []Tool {
	return c.tools
}

// ServerInfo returns the cached server info from Init.
func (c *Client) Info() *ServerInfo {
	return c.serverInfo
}

// serve implements a synchronous send/recv over the stdio transport.
func (c *Client) send(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	c.mu.Unlock()

	msg := jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
	}

	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("marshal params for %q: %w", method, err)
		}
		msg.Params = raw
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal request %q: %w", method, err)
	}

	// Write with Content-Length framing
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := fmt.Fprint(c.stdin, header); err != nil {
		return nil, fmt.Errorf("write header for %q: %w", method, err)
	}
	if _, err := fmt.Fprint(c.stdin, string(body)); err != nil {
		return nil, fmt.Errorf("write body for %q: %w", method, err)
	}

	// Read response
	resp, err := c.readMessage()
	if err != nil {
		return nil, fmt.Errorf("read response for %q: %w", method, err)
	}

	var rpcResp jsonrpcMessage
	if err := json.Unmarshal(resp, &rpcResp); err != nil {
		return nil, fmt.Errorf("parse response for %q: %w", method, err)
	}

	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}

	return rpcResp.Result, nil
}

// readMessage reads one Content-Length framed message.
func (c *Client) readMessage() ([]byte, error) {
	// Use bufio-style reader via bufio.Reader
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1)
	
	var contentLength int
	for {
		// Read one byte at a time until we've parsed headers
		n, err := c.stdout.Read(tmp)
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		if n == 0 {
			continue
		}
		b := tmp[0]
		
		if b == '\n' {
			line := strings.TrimRight(string(buf), "\r")
			buf = buf[:0]
			if line == "" {
				break // end of headers
			}
			if strings.HasPrefix(line, "Content-Length: ") {
				fmt.Sscanf(line, "Content-Length: %d", &contentLength)
			}
		} else {
			buf = append(buf, b)
		}
	}

	if contentLength == 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(c.stdout, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}
