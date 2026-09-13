package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// requestTimeout bounds how long Send waits for the matching response before
// giving up. Without it a lost response would block the caller forever.
const requestTimeout = 30 * time.Second

// Conn manages a JSON-RPC connection over stdio with Content-Length framing.
//
// Exactly one goroutine (started by StartReader) reads from the stream. All
// requests are correlated by a unique numeric id and delivered to the
// pending-request channel registered under that id.
type Conn struct {
	stdin  io.WriteCloser
	reader *bufio.Reader

	// writeMu serializes writes so concurrent requests never interleave
	// their Content-Length frames.
	writeMu sync.Mutex

	// readerOnce guarantees that only one reader goroutine is ever started.
	readerOnce sync.Once

	// nextID is the atomic source of request ids. The first id is 1.
	nextID atomic.Int64

	// mu guards closed and pending.
	mu      sync.Mutex
	closed  bool
	pending map[int]chan json.RawMessage

	// done is closed exactly once by Close. Waiters select on it so they are
	// released as soon as the connection goes away.
	done chan struct{}

	// diagChan receives publishDiagnostics notifications from the LSP server.
	// It is created in the constructor and never replaced, so it is safe to
	// read concurrently.
	diagChan chan diagnosticPush
}

type diagnosticPush struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// NewConn creates a Conn wrapping the given stdin/stdout.
func NewConn(stdin io.WriteCloser, stdout io.Reader) *Conn {
	return &Conn{
		stdin:    stdin,
		reader:   bufio.NewReader(stdout),
		pending:  make(map[int]chan json.RawMessage),
		done:     make(chan struct{}),
		diagChan: make(chan diagnosticPush, 10),
	}
}

// Send writes a JSON-RPC request and waits for its response.
//
// The request is assigned a unique id, registered while in flight, then
// written to the stream. Send returns the raw "result" field of the matching
// response, an error for a JSON-RPC error response, a timeout error after
// requestTimeout, or a connection-closed error if Close runs first.
func (c *Conn) Send(method string, params any) (json.RawMessage, error) {
	id := int(c.nextID.Add(1))

	// The buffered channel is only ever written by the reader goroutine.
	// A size of 1 lets the reader deposit a late response after the waiter
	// has already timed out, without blocking the reader.
	respCh := make(chan json.RawMessage, 1)
	if err := c.register(id, respCh); err != nil {
		return nil, err
	}
	defer c.unregister(id)

	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
		"id":      id,
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Start (or reuse) the single reader before waiting for a response, so
	// requests work even when Initialize's explicit StartReader has not run.
	c.StartReader()

	if err := c.writeMessage(body); err != nil {
		return nil, err
	}

	timer := time.NewTimer(requestTimeout)
	defer timer.Stop()

	select {
	case raw := <-respCh:
		return decodeResponse(method, raw)
	case <-c.done:
		return nil, fmt.Errorf("connection closed while awaiting response to %q", method)
	case <-timer.C:
		return nil, fmt.Errorf("request %q timed out after %s", method, requestTimeout)
	}
}

// Notify sends a JSON-RPC notification (no ID, no response expected).
func (c *Conn) Notify(method string, params any) error {
	if c.isClosed() {
		return fmt.Errorf("connection closed")
	}

	msg := map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal notification: %w", err)
	}

	return c.writeMessage(body)
}

// register records the response channel for id. It fails if the connection is
// already closed, so Send never waits on a reader that cannot deliver.
func (c *Conn) register(id int, ch chan json.RawMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("connection closed")
	}
	c.pending[id] = ch
	return nil
}

// unregister removes the response channel for id. It is safe to call for an
// unknown id and safe to call after Close.
func (c *Conn) unregister(id int) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// isClosed reports whether Close has run.
func (c *Conn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// writeMessage writes a complete JSON-RPC message with Content-Length framing.
// The write mutex keeps concurrent messages from interleaving.
func (c *Conn) writeMessage(body []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := c.stdin.Write(body); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// readMessage reads one LSP message from the stream (Content-Length framed).
// It must only be called from the single reader goroutine.
func (c *Conn) readMessage() ([]byte, error) {
	var contentLength int
	for {
		line, err := c.reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("read header: %w", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // end of headers
		}
		if strings.HasPrefix(line, "Content-Length: ") {
			contentLength, err = strconv.Atoi(line[len("Content-Length: "):])
			if err != nil {
				return nil, fmt.Errorf("parse Content-Length: %w", err)
			}
		}
	}

	if contentLength == 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(c.reader, body); err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	return body, nil
}

// markClosed marks the connection as closed and releases every waiter exactly
// once. It reports whether this call performed the transition.
func (c *Conn) markClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.closed = true
	close(c.done)
	c.pending = nil
	return true
}

// Close shuts down the connection. It is safe to call concurrently and more
// than once: only the first call closes the underlying stdin and releases the
// pending waiters.
func (c *Conn) Close() error {
	if !c.markClosed() {
		return nil
	}
	return c.stdin.Close()
}

// StartReader launches the single background goroutine that reads messages
// from the connection. It is idempotent: repeated calls are no-ops, so only
// one goroutine ever touches the reader. Call once after Initialize, or rely
// on Send starting it lazily.
func (c *Conn) StartReader() {
	c.readerOnce.Do(func() {
		go c.readLoop()
	})
}

// readLoop reads and routes messages until the stream fails or the connection
// closes. It is the only reader of the stream.
func (c *Conn) readLoop() {
	for {
		body, err := c.readMessage()
		if err != nil {
			// A dead stream must release the waiters (and make later Sends fail
			// immediately) instead of letting every call wait for the timeout.
			if wasOpen := c.markClosed(); wasOpen {
				log.Printf("lsp: reader stopped: %v", err)
			}
			return
		}
		c.dispatch(body)
	}
}

// dispatch routes one raw message: notifications to their handlers, responses
// to the pending request registered under their id. Malformed or unmatched
// messages are logged and dropped rather than treated as fatal.
func (c *Conn) dispatch(body []byte) {
	var base struct {
		ID     json.RawMessage `json:"id"`
		Result json.RawMessage `json:"result"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &base); err != nil {
		log.Printf("lsp: drop unparseable message: %v", err)
		return
	}

	// Notifications carry no non-null id; route the known ones.
	if base.Method != "" && !hasID(base.ID) {
		if base.Method == "textDocument/publishDiagnostics" {
			var push diagnosticPush
			if err := json.Unmarshal(base.Params, &push); err != nil {
				log.Printf("lsp: parse publishDiagnostics: %v", err)
				return
			}
			select {
			case c.diagChan <- push:
			default:
				// Channel full: drop rather than block the reader.
			}
		}
		return
	}

	// A message with both a method and an id is a request coming *from* the
	// server (workspace/configuration, client/registerCapability, ...). It must
	// be answered, otherwise the server waits for a reply forever.
	if base.Method != "" && hasID(base.ID) {
		c.answerServerRequest(base.Method, base.ID, base.Params)
		return
	}

	// Responses carry an id; deliver the whole message to the waiter.
	id, ok := parseID(base.ID)
	if !ok {
		log.Printf("lsp: response with unsupported id %s", base.ID)
		return
	}

	c.mu.Lock()
	ch, found := c.pending[id]
	c.mu.Unlock()
	if !found {
		log.Printf("lsp: no pending request for id %d", id)
		return
	}

	select {
	case ch <- json.RawMessage(body):
	default:
		// The waiter timed out but has not unregistered yet; drop the
		// response instead of blocking the reader.
	}
}

// answerServerRequest replies to a request initiated by the LSP server. The
// reply is intentionally permissive: an unsupported request gets a null result
// rather than an error, because most of these requests are optional and a
// missing reply would stall the server.
func (c *Conn) answerServerRequest(method string, id, params json.RawMessage) {
	var result any
	switch method {
	case "workspace/configuration":
		// One (null) entry per requested section.
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(params, &p)
		result = make([]any, len(p.Items))
	case "window/workDoneProgress/create",
		"client/registerCapability",
		"client/unregisterCapability",
		"workspace/workspaceFolders",
		"window/showMessageRequest":
		result = nil
	default:
		log.Printf("lsp: answering unsupported server request %q with null", method)
		result = nil
	}

	reply, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
	if err != nil {
		log.Printf("lsp: marshal reply to %q: %v", method, err)
		return
	}
	if err := c.writeMessage(reply); err != nil {
		log.Printf("lsp: write reply to %q: %v", method, err)
	}
}

// hasID reports whether a JSON-RPC id field is present and non-null.
func hasID(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null"
}

// parseID converts a JSON-RPC id (number or numeric string) into the pending
// map key. It reports false for ids this client never issues.
func parseID(raw json.RawMessage) (int, bool) {
	if !hasID(raw) {
		return 0, false
	}

	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, true
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if n, err := strconv.Atoi(s); err == nil {
			return n, true
		}
	}
	return 0, false
}

// decodeResponse extracts the result of a JSON-RPC response, converting a
// JSON-RPC error object into a Go error.
func decodeResponse(method string, raw json.RawMessage) (json.RawMessage, error) {
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("parse response to %q: %w", method, err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("LSP error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp.Result, nil
}
