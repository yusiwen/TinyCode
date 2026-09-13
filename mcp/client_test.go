package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// frameStream parses Content-Length framed messages from a stream while
// preserving buffered read-ahead between calls.
type frameStream struct {
	br *bufio.Reader
}

func newFrameStream(r io.Reader) *frameStream {
	return &frameStream{br: bufio.NewReader(r)}
}

func (f *frameStream) read() (string, error) {
	length := -1
	for {
		line, err := f.br.ReadString('\n')
		if err != nil {
			return "", err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return "", err
		}
		length = n
	}
	if length < 0 {
		return "", fmt.Errorf("missing Content-Length header")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(f.br, body); err != nil {
		return "", err
	}
	return string(body), nil
}

func writeFrame(w io.Writer, body string) error {
	_, err := io.WriteString(w, fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body))
	return err
}

func closePipes(closers ...io.Closer) {
	for _, c := range closers {
		_ = c.Close()
	}
}

// TestReadMessageRejectsNegativeContentLength guards against a negative
// Content-Length, which used to reach make([]byte, n) and panic.
func TestReadMessageRejectsNegativeContentLength(t *testing.T) {
	client := NewClient(io.Discard, strings.NewReader("Content-Length: -5\r\n\r\n"), nil)
	if _, err := client.readMessage(); err == nil {
		t.Fatal("expected error for negative Content-Length, got nil")
	}
}

// TestReadMessageRejectsOversizedContentLength ensures an absurd length is
// rejected before allocating the body.
func TestReadMessageRejectsOversizedContentLength(t *testing.T) {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", maxMessageSize+1)
	client := NewClient(io.Discard, strings.NewReader(header), nil)

	_, err := client.readMessage()
	if err == nil {
		t.Fatal("expected error for oversized Content-Length, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("expected limit error, got: %v", err)
	}
}

// TestReadMessageHeaderParsing covers case-insensitive header names and
// unknown headers that must be ignored.
func TestReadMessageHeaderParsing(t *testing.T) {
	raw := "x-trace-id: abc\r\ncontent-length: 2\r\nx-another: 1\r\n\r\n{}"
	client := NewClient(io.Discard, strings.NewReader(raw), nil)

	body, err := client.readMessage()
	if err != nil {
		t.Fatalf("readMessage: %v", err)
	}
	if string(body) != "{}" {
		t.Fatalf("expected body %q, got %q", "{}", string(body))
	}
}

// TestSendSkipsNotificationBeforeResponse ensures an id-less notification
// arriving before the response does not desynchronize the exchange.
func TestSendSkipsNotificationBeforeResponse(t *testing.T) {
	clientStdinR, clientStdinW := io.Pipe()
	serverStdoutR, serverStdoutW := io.Pipe()
	defer closePipes(clientStdinR, clientStdinW, serverStdoutR, serverStdoutW)

	client := NewClient(clientStdinW, serverStdoutR, nil)

	go func() {
		fs := newFrameStream(clientStdinR)
		if _, err := fs.read(); err != nil {
			return
		}
		_ = writeFrame(serverStdoutW, `{"jsonrpc":"2.0","method":"notifications/message","params":{"level":"info"}}`)
		_ = writeFrame(serverStdoutW, `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"notified","version":"1"}}}`)
	}()

	info, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize after notification: %v", err)
	}
	if info.Name != "notified" {
		t.Fatalf("expected server %q, got %q", "notified", info.Name)
	}
}

// TestSendSkipsMismatchedResponseID ensures a response addressed to another
// request is not returned as the answer to this one.
func TestSendSkipsMismatchedResponseID(t *testing.T) {
	clientStdinR, clientStdinW := io.Pipe()
	serverStdoutR, serverStdoutW := io.Pipe()
	defer closePipes(clientStdinR, clientStdinW, serverStdoutR, serverStdoutW)

	client := NewClient(clientStdinW, serverStdoutR, nil)

	go func() {
		fs := newFrameStream(clientStdinR)
		if _, err := fs.read(); err != nil {
			return
		}
		_ = writeFrame(serverStdoutW, `{"jsonrpc":"2.0","id":42,"result":{"serverInfo":{"name":"wrong","version":"1"}}}`)
		_ = writeFrame(serverStdoutW, `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"right","version":"1"}}}`)
	}()

	info, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize after mismatched response: %v", err)
	}
	if info.Name != "right" {
		t.Fatalf("expected server %q, got %q", "right", info.Name)
	}
}

// TestReadMatchingResponseBoundsUnrelatedMessages ensures a server that keeps
// sending unrelated frames cannot make the reader spin forever.
func TestReadMatchingResponseBoundsUnrelatedMessages(t *testing.T) {
	notification := `{"jsonrpc":"2.0","method":"notifications/message"}`
	correct := `{"jsonrpc":"2.0","id":1,"result":{}}`

	var sb strings.Builder
	for i := 0; i < maxSkippedMessages+2; i++ {
		fmt.Fprintf(&sb, "Content-Length: %d\r\n\r\n%s", len(notification), notification)
	}
	fmt.Fprintf(&sb, "Content-Length: %d\r\n\r\n%s", len(correct), correct)

	client := NewClient(io.Discard, strings.NewReader(sb.String()), nil)
	if _, err := client.readMatchingResponse(1); err == nil {
		t.Fatal("expected error after exceeding the skip budget, got nil")
	}
}

// TestConcurrentSendCorrelatesResponses runs several concurrent exchanges
// against a server that batches requests and answers them in reverse order.
// Without id correlation a caller would receive another caller's result.
func TestConcurrentSendCorrelatesResponses(t *testing.T) {
	clientStdinR, clientStdinW := io.Pipe()
	serverStdoutR, serverStdoutW := io.Pipe()
	defer closePipes(clientStdinR, clientStdinW, serverStdoutR, serverStdoutW)

	client := NewClient(clientStdinW, serverStdoutR, nil)

	requests := make(chan jsonrpcMessage, 16)
	go func() {
		fs := newFrameStream(clientStdinR)
		for {
			body, err := fs.read()
			if err != nil {
				return
			}
			var req jsonrpcMessage
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				return
			}
			requests <- req
		}
	}()

	// Batch requests that arrive close together and answer them in reverse
	// order. Only id-based correlation can route such replies correctly.
	go func() {
		var pending []jsonrpcMessage
		for {
			select {
			case req := <-requests:
				pending = append(pending, req)
				continue
			case <-time.After(100 * time.Millisecond):
			}
			for i := len(pending) - 1; i >= 0; i-- {
				resp := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, pending[i].ID, string(pending[i].Params))
				if err := writeFrame(serverStdoutW, resp); err != nil {
					return
				}
			}
			pending = pending[:0]
		}
	}()

	const senders = 4
	var wg sync.WaitGroup
	errCh := make(chan error, senders)
	for i := 0; i < senders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			raw, err := client.send(ctx, "tools/call", map[string]int{"value": i})
			if err != nil {
				errCh <- fmt.Errorf("sender %d: %w", i, err)
				return
			}
			var res struct {
				Value int `json:"value"`
			}
			if err := json.Unmarshal(raw, &res); err != nil {
				errCh <- fmt.Errorf("sender %d: parse result: %w", i, err)
				return
			}
			if res.Value != i {
				errCh <- fmt.Errorf("sender %d received response for value %d", i, res.Value)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}
