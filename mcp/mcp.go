package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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

// errNoMatchingResponse is delivered to a waiter whose response never arrived
// because the peer kept sending unrelated frames (notifications or responses
// addressed to other requests) beyond maxSkippedMessages.
var errNoMatchingResponse = errors.New("no matching response within the skip budget")

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
//
// Exactly one goroutine (started lazily by the first request) reads from the
// stream. Concurrent requests are correlated by a unique numeric id and each
// response is delivered to the channel registered under that id, so many calls
// can be in flight at once without interleaving frames or stealing responses.
type Client struct {
	stdin  io.Writer
	stdout io.Reader
	stderr io.Reader

	// writeMu serializes frame writes. It is held only while a single
	// Content-Length frame is written, never while waiting for a response, so
	// one slow call cannot block another caller's write.
	writeMu sync.Mutex

	// readerOnce guarantees that only one reader goroutine is ever started.
	readerOnce sync.Once

	// mu guards nextID, pending, closed, closeCause and kill.
	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage
	closed  bool
	kill    func()

	// closeCause records why the client was marked closed, if not by Close.
	closeCause error

	// done is closed exactly once when the client is closed. Waiters select on
	// it so they are released as soon as the transport goes away.
	done chan struct{}

	closeOnce sync.Once
	closeErr  error

	// metaMu guards serverInfo and tools, which Initialize/ListTools write and
	// Tools/Info read; calls may overlap with other requests.
	metaMu     sync.Mutex
	serverInfo *ServerInfo
	tools      []Tool
}

// NewClient creates an MCP client using the given stdio/stderr streams.
func NewClient(stdin io.Writer, stdout io.Reader, stderr io.Reader) *Client {
	return &Client{
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		nextID:  1,
		pending: make(map[int]chan json.RawMessage),
		done:    make(chan struct{}),
	}
}

// SetKillFunc registers a transport-level termination function, for example
// killing the stdio child process. It is invoked by Close so a read blocked on
// a dead peer is unblocked and the goroutine can exit.
func (c *Client) SetKillFunc(fn func()) {
	c.mu.Lock()
	c.kill = fn
	c.mu.Unlock()
}

// Close releases the transport and fails every pending waiter. It is safe to
// call multiple times and from any goroutine, including while a request is in
// flight: only the first call tears the transport down.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		kill := c.kill
		c.mu.Unlock()

		// Release every waiter before touching the transport so a blocked send
		// returns promptly even when kill waits for a child to be reaped.
		c.markClosed(errClientClosed)

		// When a kill function is registered the transport owner (for example
		// the stdio subprocess wrapper) is responsible for tearing down the
		// underlying process and pipes. Its contract is to return only once the
		// child has been killed and reaped.
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

// markClosed transitions the client to the closed state exactly once. The done
// channel is closed so pending waiters wake up, and the reason is retained for
// diagnostics. It reports whether this call performed the transition.
func (c *Client) markClosed(cause error) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.closed = true
	c.closeCause = cause
	close(c.done)
	return true
}

// startReader launches the single background goroutine that reads and routes
// messages. It is idempotent: repeated calls are no-ops, so only one goroutine
// ever touches the stream.
func (c *Client) startReader() {
	c.readerOnce.Do(func() {
		go c.readLoop()
	})
}

// readLoop reads frames until the stream fails, routing each one to its waiter.
// It is the only reader of c.stdout.
func (c *Client) readLoop() {
	skipped := 0
	for {
		raw, err := c.readMessage()
		if err != nil {
			// A dead stream must release the waiters (and make later sends fail
			// immediately) instead of letting every call hang forever.
			c.markClosed(fmt.Errorf("mcp reader stopped: %w", err))
			return
		}

		matched, pending := c.dispatch(raw)
		if matched {
			skipped = 0
			continue
		}
		if !pending {
			// Nothing is waiting, so an unmatched frame is simply dropped and
			// must not count against a future request's budget.
			skipped = 0
			continue
		}
		skipped++
		if skipped > maxSkippedMessages {
			// The peer is flooding unrelated frames while a request is pending.
			// Fail the waiters rather than reading the stream forever; the
			// stream itself stays framed, so a later call can still succeed.
			c.failPending()
			skipped = 0
		}
	}
}

// dispatch routes one raw frame to the pending request registered under its id.
// Notifications and responses addressed to unknown ids are dropped. It reports
// whether a pending request matched and whether any request was pending at all.
func (c *Client) dispatch(raw []byte) (matched, pending bool) {
	var msg jsonrpcMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return false, c.hasPending()
	}
	// A method means this is a notification or a server-initiated request, not
	// a response to one of ours. Server request ids share our id space, so they
	// must never be mistaken for a response.
	if msg.Method != "" {
		if msg.ID != 0 {
			// A request from the server expects an answer; dropping it leaves
			// the server waiting forever.
			c.answerServerRequest(msg.Method, msg.ID)
		}
		return false, c.hasPending()
	}
	if msg.ID == 0 {
		return false, c.hasPending()
	}

	c.mu.Lock()
	ch, ok := c.pending[msg.ID]
	pending = len(c.pending) > 0
	c.mu.Unlock()
	if !ok {
		return false, pending
	}

	// The channel is buffered with room for one response, so a waiter that
	// already gave up cannot block the reader.
	select {
	case ch <- json.RawMessage(raw):
	default:
	}
	return true, pending
}

// answerServerRequest replies to a request initiated by the MCP server. The
// replies are deliberately minimal: the methods this client implements are
// answered, everything else gets a "method not found" error so the server is
// never left waiting. Write errors are logged and swallowed because this runs on
// the single reader goroutine, which must not die because of one bad reply.
func (c *Client) answerServerRequest(method string, id int) {
	resp := jsonrpcMessage{JSONRPC: "2.0", ID: id}
	switch method {
	case "ping":
		resp.Result = json.RawMessage(`{}`)
	case "roots/list":
		resp.Result = json.RawMessage(`{"roots":[]}`)
	default:
		resp.Error = &jsonrpcError{Code: -32601, Message: "method not found: " + method}
	}

	body, err := json.Marshal(resp)
	if err != nil {
		log.Printf("mcp: marshal reply to %q: %v", method, err)
		return
	}
	if err := c.writeFrame(body); err != nil {
		log.Printf("mcp: write reply to %q: %v", method, err)
	}
}

// hasPending reports whether any request is currently registered.
func (c *Client) hasPending() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending) > 0
}

// failPending closes the response channel of every pending request so the
// callers return an error. Closing is only ever done here, by the single reader
// goroutine, so no channel is ever closed twice.
func (c *Client) failPending() {
	c.mu.Lock()
	stale := c.pending
	c.pending = make(map[int]chan json.RawMessage)
	c.mu.Unlock()

	for _, ch := range stale {
		close(ch)
	}
}

// reserve allocates the next unique request id and registers a response channel
// for it under the same lock, so ids can never be reused while in flight and no
// response can arrive before its waiter is registered.
func (c *Client) reserve() (int, chan json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, nil, errClientClosed
	}
	id := c.nextID
	c.nextID++
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
	return id, ch, nil
}

// unregister removes the response channel for id. It is safe to call for an
// unknown id and after the client has been closed.
func (c *Client) unregister(id int) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
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
	c.metaMu.Lock()
	c.serverInfo = &result.ServerInfo
	c.metaMu.Unlock()
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
	c.metaMu.Lock()
	c.tools = result.Tools
	c.metaMu.Unlock()
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
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	return append([]Tool(nil), c.tools...)
}

// ServerInfo returns the cached server info from Init.
func (c *Client) Info() *ServerInfo {
	c.metaMu.Lock()
	defer c.metaMu.Unlock()
	if c.serverInfo == nil {
		return nil
	}
	info := *c.serverInfo
	return &info
}

// send performs one request/response exchange over the stdio transport.
//
// The request id is reserved and the response channel registered before the
// frame is written, so a response can never race ahead of its waiter. Any
// number of exchanges may be in flight concurrently: writes are serialized by
// writeMu, while waiting is done on the request's own channel.
//
// If ctx is cancelled the request is unregistered and the context error is
// returned; the transport is deliberately left intact so the client (and every
// other in-flight call) stays usable.
func (c *Client) send(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("send %q: %w", method, err)
	}

	id, respCh, err := c.reserve()
	if err != nil {
		return nil, fmt.Errorf("send %q: %w", method, err)
	}
	defer c.unregister(id)

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

	// Start (or reuse) the single reader before waiting for a response.
	c.startReader()

	if err := c.writeFrame(body); err != nil {
		return nil, fmt.Errorf("write request %q: %w", method, err)
	}

	select {
	case raw, ok := <-respCh:
		if !ok {
			// The reader closed the channel after failing the waiter, e.g. the
			// peer flooded unrelated frames past the skip budget.
			return nil, fmt.Errorf("read response for %q: %w", method, errNoMatchingResponse)
		}
		var rpcResp jsonrpcMessage
		if err := json.Unmarshal(raw, &rpcResp); err != nil {
			return nil, fmt.Errorf("parse response for %q: %w", method, err)
		}
		if rpcResp.Error != nil {
			return nil, rpcResp.Error
		}
		return rpcResp.Result, nil
	case <-ctx.Done():
		// Cancel this call only. Unregistering (the deferred call) drops any
		// late response, and the client remains open for later requests.
		return nil, fmt.Errorf("read response for %q: %w", method, ctx.Err())
	case <-c.done:
		return nil, fmt.Errorf("read response for %q: %w", method, c.closeReason())
	}
}

// closeReason returns why the client was closed: the recorded cause when the
// transport died on its own, otherwise errClientClosed.
func (c *Client) closeReason() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeCause != nil {
		return c.closeCause
	}
	return errClientClosed
}

// writeFrame writes one complete Content-Length framed message. writeMu keeps
// concurrent frames from interleaving, and the frame is written with a single
// Write so a peer never observes a header without its body.
func (c *Client) writeFrame(body []byte) error {
	frame := make([]byte, 0, len(body)+32)
	frame = append(frame, fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))...)
	frame = append(frame, body...)

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	_, err := c.stdin.Write(frame)
	return err
}

// readMessage reads one Content-Length framed message. It must only be called
// from the single reader goroutine started by startReader.
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
