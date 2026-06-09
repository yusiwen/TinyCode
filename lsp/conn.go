package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Conn manages a JSON-RPC connection over stdio with Content-Length framing.
type Conn struct {
	stdin  io.WriteCloser
	reader *bufio.Reader
	closed bool

	// diagChan receives publishDiagnostics notifications from the LSP server.
	// Lazily initialized on first use.
	diagChan chan diagnosticPush
}

type diagnosticPush struct {
	URI         string      `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// NewConn creates a Conn wrapping the given stdin/stdout.
func NewConn(stdin io.WriteCloser, stdout io.Reader) *Conn {
	return &Conn{
		stdin:  stdin,
		reader: bufio.NewReader(stdout),
	}
}

// Send writes a JSON-RPC request and returns the decoded response.
// For requests with an ID (calls), it waits for the matching response.
// For notifications (no ID), it returns immediately with nil.
func (c *Conn) Send(method string, params any) (json.RawMessage, error) {
	if c.closed {
		return nil, fmt.Errorf("connection closed")
	}

	id := 1
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

	if err := c.writeMessage(body); err != nil {
		return nil, err
	}

	return c.readResponse()
}

// Notify sends a JSON-RPC notification (no ID, no response expected).
func (c *Conn) Notify(method string, params any) error {
	if c.closed {
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

// writeMessage writes a complete JSON-RPC message with Content-Length framing.
func (c *Conn) writeMessage(body []byte) error {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := c.stdin.Write([]byte(header)); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := c.stdin.Write(body); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}

// readResponse reads a JSON-RPC response message, routing notifications to handlers.
func (c *Conn) readResponse() (json.RawMessage, error) {
	for {
		body, err := c.readMessage()
		if err != nil {
			return nil, err
		}

		var base struct {
			ID     any             `json:"id"`
			Result json.RawMessage `json:"result"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}

		if err := json.Unmarshal(body, &base); err != nil {
			return nil, fmt.Errorf("parse response: %w", err)
		}

		// Route publishDiagnostics notifications to channel
		if base.Method == "textDocument/publishDiagnostics" {
			if c.diagChan == nil {
				c.diagChan = make(chan diagnosticPush, 10)
			}
			var push diagnosticPush
			if err := json.Unmarshal(base.Params, &push); err == nil {
				select {
				case c.diagChan <- push:
				default:
					// channel full, drop
				}
			}
			continue
		}

		// Skip other notifications (no ID, have Method)
		if base.Method != "" && base.ID == nil {
			continue
		}

		if base.Error != nil {
			return nil, fmt.Errorf("LSP error %d: %s", base.Error.Code, base.Error.Message)
		}

		return base.Result, nil
	}
}

// readMessage reads one LSP message from the stream (Content-Length framed).
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

// Close shuts down the connection.
func (c *Conn) Close() error {
	c.closed = true
	return c.stdin.Close()
}

// StartReader launches a background goroutine that reads messages from
// the connection and routes notifications (like publishDiagnostics) to
// the appropriate channels. Call once after Initialize.
func (c *Conn) StartReader() {
	go func() {
		for {
			body, err := c.readMessage()
			if err != nil {
				return
			}
			var base struct {
				ID     any             `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(body, &base); err != nil {
				continue
			}
			// Route publishDiagnostics to channel
			if base.Method == "textDocument/publishDiagnostics" {
				if c.diagChan == nil {
					c.diagChan = make(chan diagnosticPush, 10)
				}
				var push diagnosticPush
				if json.Unmarshal(base.Params, &push) == nil {
					select {
					case c.diagChan <- push:
					default:
					}
				}
			}
			// Ignore other notifications and unexpected responses
		}
	}()
}
