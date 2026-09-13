package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const (
	// maxMessageSize caps a single Content-Length framed JSON-RPC message.
	// 8 MiB is far above any realistic MCP payload while preventing a hostile
	// or broken server from forcing an unbounded allocation.
	maxMessageSize = 8 << 20

	// maxHeaderBytes bounds a single header line while parsing Content-Length
	// framing, so a peer that never sends a newline cannot grow memory forever.
	maxHeaderBytes = 8 << 10

	// maxSkippedMessages bounds how many unrelated frames (notifications or
	// responses addressed to another request) are discarded while waiting for
	// the response matching the current request id.
	maxSkippedMessages = 100
)

// errClientClosed is returned once the client has been closed.
var errClientClosed = errors.New("mcp client is closed")

// Tool represents a tool exposed by an MCP server.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
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
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
	Blob     string `json:"blob,omitempty"`
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
	Initialize(ctx context.Context) (*ServerInfo, error)
	ListTools(ctx context.Context) ([]Tool, error)
	CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error)
	ListResources(ctx context.Context) ([]Resource, error)
	ReadResource(ctx context.Context, uri string) (*ResourceResult, error)
	Tools() []Tool
	Info() *ServerInfo
	Close() error
}

// Client manages a JSON-RPC 2.0 connection to an MCP server over stdio.
type Client struct {
	stdin  io.Writer
	stdout io.Reader
	stderr io.Reader

	// mu serializes a complete request/response exchange: from reserving the
	// request id and writing the frame to reading the matching response. This
	// keeps concurrent callers from interleaving frames or stealing each
	// other's responses.
	mu     sync.Mutex
	nextID int

	// stateMu guards closed, kill and closeErr. It is deliberately separate
	// from mu so Close can tear down a blocked exchange without waiting for it.
	stateMu sync.Mutex
	closed  bool
	kill    func()

	closeOnce sync.Once
	closeErr  error

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

// SetKillFunc registers a transport-level termination function, for example
// killing the stdio child process. It is invoked by Close and whenever an
// in-flight exchange is cancelled, so a read blocked on a dead peer is
// unblocked and the goroutine can exit.
func (c *Client) SetKillFunc(fn func()) {
	c.stateMu.Lock()
	c.kill = fn
	c.stateMu.Unlock()
}

// Close releases the transport. It is safe to call multiple times and from
// any goroutine, including while a request is in flight.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.stateMu.Lock()
		c.closed = true
		kill := c.kill
		c.stateMu.Unlock()

		// When a kill function is registered the transport owner (for example
		// the stdio subprocess wrapper) is responsible for tearing down the
		// underlying process and pipes.
		if kill != nil {
			kill()
			return
		}
		if closer, ok := c.stdin.(io.Closer); ok {
			c.closeErr = closer.Close()
		}
	})
	return c.closeErr
}

// Initialize sends the initialize request and waits for a response.
func (c *Client) Initialize(ctx context.Context) (*ServerInfo, error) {
	params := map[string]any{
		"protocolVersion": "2025-03-26",
		"clientInfo": map[string]string{
			"name":    "tinycode",
			"version": "0.0.4",
		},
	}
	raw, err := c.send(ctx, "initialize", params)
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
func (c *Client) ListTools(ctx context.Context) ([]Tool, error) {
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

// CallTool invokes a tool by name with the given arguments.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	params := map[string]any{
		"name":      name,
		"arguments": args,
	}
	raw, err := c.send(ctx, "tools/call", params)
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
func (c *Client) ListResources(ctx context.Context) ([]Resource, error) {
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

// ReadResource reads the content of a resource identified by its URI.
func (c *Client) ReadResource(ctx context.Context, uri string) (*ResourceResult, error) {
	params := map[string]any{
		"uri": uri,
	}
	raw, err := c.send(ctx, "resources/read", params)
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

// send performs one synchronous request/response exchange over the stdio
// transport. The whole exchange is serialized by c.mu so concurrent callers
// cannot interleave frames or read each other's responses.
func (c *Client) send(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("send %q: %w", method, err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.stateMu.Lock()
	closed := c.closed
	c.stateMu.Unlock()
	if closed {
		return nil, fmt.Errorf("send %q: %w", method, errClientClosed)
	}
	// The context may have been cancelled while waiting for the exchange lock.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("send %q: %w", method, err)
	}

	id := c.nextID
	c.nextID++

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

	// Build the whole frame first and write it with a single Write so a peer
	// never observes a header without its body.
	frame := make([]byte, 0, len(body)+32)
	frame = append(frame, fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))...)
	frame = append(frame, body...)
	if _, err := c.stdin.Write(frame); err != nil {
		return nil, fmt.Errorf("write request %q: %w", method, err)
	}

	return c.readResponse(ctx, method, id)
}

// readResponse reads frames until the response for id arrives, skipping
// notifications and responses addressed to other requests.
func (c *Client) readResponse(ctx context.Context, method string, id int) (json.RawMessage, error) {
	type readResult struct {
		msg json.RawMessage
		err error
	}
	done := make(chan readResult, 1)

	go func() {
		msg, err := c.readMatchingResponse(id)
		done <- readResult{msg: msg, err: err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			return nil, fmt.Errorf("read response for %q: %w", method, res.err)
		}
		var rpcResp jsonrpcMessage
		if err := json.Unmarshal(res.msg, &rpcResp); err != nil {
			return nil, fmt.Errorf("parse response for %q: %w", method, err)
		}
		if rpcResp.Error != nil {
			return nil, rpcResp.Error
		}
		return rpcResp.Result, nil
	case <-ctx.Done():
		// The read is still blocked. Tear the transport down so the reader
		// goroutine cannot leak and the stream is not silently desynchronized.
		_ = c.Close()
		return nil, fmt.Errorf("read response for %q: %w", method, ctx.Err())
	}
}

// readMatchingResponse returns the raw frame whose JSON-RPC id equals id.
// Notifications (no id) and responses addressed to other requests are skipped
// up to maxSkippedMessages frames.
func (c *Client) readMatchingResponse(id int) ([]byte, error) {
	for skipped := 0; skipped < maxSkippedMessages; skipped++ {
		raw, err := c.readMessage()
		if err != nil {
			return nil, err
		}
		var msg jsonrpcMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, fmt.Errorf("parse message: %w", err)
		}
		if msg.ID == id {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("no response for request id %d after %d unrelated messages", id, maxSkippedMessages)
}

// readMessage reads one Content-Length framed message.
func (c *Client) readMessage() ([]byte, error) {
	contentLength := -1

	buf := make([]byte, 0, 256)
	tmp := make([]byte, 1)
	for {
		// Read one byte at a time until we've parsed headers.
		n, err := c.stdout.Read(tmp)
		if err != nil {
			return nil, fmt.Errorf("read: %w", err)
		}
		if n == 0 {
			continue
		}
		b := tmp[0]

		if b != '\n' {
			buf = append(buf, b)
			if len(buf) > maxHeaderBytes {
				return nil, fmt.Errorf("header line exceeds %d bytes", maxHeaderBytes)
			}
			continue
		}

		line := strings.TrimRight(string(buf), "\r")
		buf = buf[:0]
		if line == "" {
			break // end of headers
		}

		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue // ignore malformed header lines
		}
		// Header names are case-insensitive; unknown headers are ignored.
		if !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}

		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("invalid Content-Length header %q: %w", value, err)
		}
		contentLength = parsed
	}

	if contentLength < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	if contentLength > maxMessageSize {
		return nil, fmt.Errorf("Content-Length %d exceeds limit of %d bytes", contentLength, maxMessageSize)
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(c.stdout, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}
