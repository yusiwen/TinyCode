package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// mockServer runs a minimal MCP server over pipes and returns a connected client.
// cancel() stops the goroutine.
func startMockServer(t *testing.T) (*Client, context.CancelFunc) {
	t.Helper()

	clientStdinR, clientStdinW := io.Pipe()   // client → server
	serverStdoutR, serverStdoutW := io.Pipe() // server → client

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer clientStdinR.Close()
		defer serverStdoutW.Close()

		// 1. Expect initialize
		msg := readMockMsg(t, clientStdinR)
		if !strings.Contains(msg, `"method":"initialize"`) {
			t.Errorf("expected initialize, got: %s", msg[:min(len(msg), 80)])
			return
		}
		writeMockMsg(t, serverStdoutW, `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"mock-server","version":"1.0.0"}}}`)

		// 2. Expect tools/list
		msg = readMockMsg(t, clientStdinR)
		if !strings.Contains(msg, `"method":"tools/list"`) {
			t.Errorf("expected tools/list, got: %s", msg[:min(len(msg), 80)])
			return
		}
		writeMockMsg(t, serverStdoutW, `{"jsonrpc":"2.0","id":2,"result":{"tools":[
			{"name":"echo","description":"Echo input back","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}},
			{"name":"add","description":"Add two numbers","inputSchema":{"type":"object","properties":{"a":{"type":"number"},"b":{"type":"number"}}}}
		]}}`)

		// 3. Expect tools/call echo
		msg = readMockMsg(t, clientStdinR)
		if !strings.Contains(msg, `"method":"tools/call"`) {
			t.Errorf("expected tools/call, got: %s", msg[:min(len(msg), 80)])
			return
		}
		writeMockMsg(t, serverStdoutW, `{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"hello back"}]}}`)

		<-ctx.Done()
	}()

	client := NewClient(clientStdinW, serverStdoutR, nil)
	return client, cancel
}

func readMockMsg(t *testing.T, r io.Reader) string {
	t.Helper()
	msg, err := readMockFrame(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return msg
}

// readMockFrame is readMockMsg without the t.Fatalf, for a goroutine that may
// still be reading when the test finishes: it returns the error instead.
func readMockFrame(r io.Reader) (string, error) {
	var contentLength int
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1)
	for {
		n, err := r.Read(tmp)
		if err != nil {
			return "", err
		}
		if n == 0 {
			continue
		}
		b := tmp[0]
		if b == '\n' {
			line := strings.TrimRight(string(buf), "\r")
			buf = buf[:0]
			if line == "" {
				break
			}
			if strings.HasPrefix(line, "Content-Length: ") {
				fmt.Sscanf(line, "Content-Length: %d", &contentLength)
			}
		} else {
			buf = append(buf, b)
		}
	}
	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", err
	}
	return string(body), nil
}

// startFrameServer connects a Client to one goroutine that reads request frames
// and answers each with the string reply returns ("" answers nothing). It is the
// scaffolding for a server that goes silent on a chosen method, and it uses only
// error-returning reads and writes so a goroutine still running when the test
// ends cannot fail the test from off-goroutine.
func startFrameServer(t *testing.T, reply func(req string) string) *Client {
	t.Helper()

	clientStdinR, clientStdinW := io.Pipe()
	serverStdoutR, serverStdoutW := io.Pipe()
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		for {
			req, err := readMockFrame(clientStdinR)
			if err != nil {
				return
			}
			out := reply(req)
			if out == "" {
				continue
			}
			frame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(out), out)
			if _, err := serverStdoutW.Write([]byte(frame)); err != nil {
				return
			}
		}
	}()

	client := NewClient(clientStdinW, serverStdoutR, nil)
	t.Cleanup(func() {
		clientStdinW.Close()
		serverStdoutW.Close()
		<-stopped
	})
	return client
}

func writeMockMsg(t *testing.T, w io.Writer, jsonStr string) {
	t.Helper()
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(jsonStr))
	_, err := w.Write([]byte(header + jsonStr))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestMCPInitialize(t *testing.T) {
	client, cancel := startMockServer(t)
	defer cancel()

	info, err := client.Initialize(context.Background())
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if info.Name != "mock-server" {
		t.Errorf("expected mock-server, got %q", info.Name)
	}
}

func TestMCPListTools(t *testing.T) {
	client, cancel := startMockServer(t)
	defer cancel()

	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "echo" {
		t.Errorf("expected 'echo', got %q", tools[0].Name)
	}
}

func TestMCPCallTool(t *testing.T) {
	client, cancel := startMockServer(t)
	defer cancel()

	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	result, err := client.CallTool(context.Background(), "echo", map[string]any{"text": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatal("expected content in result")
	}
	if result.Content[0].Text != "hello back" {
		t.Errorf("expected 'hello back', got %q", result.Content[0].Text)
	}
}

func TestMCPErrorResponse(t *testing.T) {
	clientStdinR, clientStdinW := io.Pipe()
	serverStdoutR, serverStdoutW := io.Pipe()

	client := NewClient(clientStdinW, serverStdoutR, nil)

	go func() {
		defer clientStdinR.Close()
		defer serverStdoutW.Close()

		msg := readMockMsg(t, clientStdinR)
		if strings.Contains(msg, `"method":"initialize"`) {
			writeMockMsg(t, serverStdoutW, `{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"Server not ready"}}`)
		}
	}()

	_, err := client.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error for server error response")
	}
	if !strings.Contains(err.Error(), "Server not ready") {
		t.Errorf("expected 'Server not ready', got: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestMCPResourcesList(t *testing.T) {
	clientStdinR, clientStdinW := io.Pipe()
	serverStdoutR, serverStdoutW := io.Pipe()

	client := NewClient(clientStdinW, serverStdoutR, nil)

	go func() {
		defer clientStdinR.Close()
		defer serverStdoutW.Close()

		msg := readMockMsg(t, clientStdinR)
		if strings.Contains(msg, `"method":"initialize"`) {
			writeMockMsg(t, serverStdoutW, `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"t","version":"1"}}}`)
		}

		msg = readMockMsg(t, clientStdinR)
		if strings.Contains(msg, `"method":"resources/list"`) {
			writeMockMsg(t, serverStdoutW, `{"jsonrpc":"2.0","id":2,"result":{"resources":[
				{"uri":"file:///data/config.json","name":"Config","description":"Server config"},
				{"uri":"file:///data/log.txt","name":"Log","description":"Server log"}
			]}}`)
		}
	}()

	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	resources, err := client.ListResources(context.Background())
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(resources))
	}
	if resources[0].Name != "Config" {
		t.Errorf("expected 'Config', got %q", resources[0].Name)
	}
}

func TestMCPReadResource(t *testing.T) {
	clientStdinR, clientStdinW := io.Pipe()
	serverStdoutR, serverStdoutW := io.Pipe()

	client := NewClient(clientStdinW, serverStdoutR, nil)

	go func() {
		defer clientStdinR.Close()
		defer serverStdoutW.Close()

		msg := readMockMsg(t, clientStdinR)
		if strings.Contains(msg, `"method":"initialize"`) {
			writeMockMsg(t, serverStdoutW, `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"t","version":"1"}}}`)
		}

		msg = readMockMsg(t, clientStdinR)
		if strings.Contains(msg, `"method":"resources/read"`) {
			writeMockMsg(t, serverStdoutW, `{"jsonrpc":"2.0","id":2,"result":{"contents":[{"uri":"file:///data/config.json","mimeType":"application/json","text":"{\"key\":\"value\"}"}]}}`)
		}
	}()

	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	result, err := client.ReadResource(context.Background(), "file:///data/config.json")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(result.Contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(result.Contents))
	}
	if !strings.Contains(result.Contents[0].Text, "key") {
		t.Errorf("expected content with 'key', got %q", result.Contents[0].Text)
	}
}

// TestMCPSendBoundsAnUnboundedRequest covers issue #2: the handshake is bounded at
// its call site, but every request after it inherits the agent's run context, which
// carries no deadline - a server that accepts tools/call and never answers used to
// block the agent step with no error surfaced. The client must stay usable, so the
// test asks for tools/list afterwards and expects an answer.
func TestMCPSendBoundsAnUnboundedRequest(t *testing.T) {
	previous := defaultRequestTimeout
	defaultRequestTimeout = 150 * time.Millisecond
	t.Cleanup(func() { defaultRequestTimeout = previous })

	client := startFrameServer(t, func(req string) string {
		switch {
		case strings.Contains(req, `"method":"initialize"`):
			return `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"silent","version":"1"}}}`
		case strings.Contains(req, `"method":"tools/list"`):
			return `{"jsonrpc":"2.0","id":3,"result":{"tools":[]}}`
		}
		return "" // tools/call: accepted, never answered
	})

	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	start := time.Now()
	_, err := client.CallTool(context.Background(), "echo", map[string]any{"text": "hi"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("CallTool against a server that never answers returned success")
	}
	if !strings.Contains(err.Error(), "no response within") {
		t.Errorf("error = %v, want it to name the default bound", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("CallTool took %s to report the bound", elapsed)
	}

	// The request was abandoned, not the transport.
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Errorf("the client stopped working after the bound fired: %v", err)
	}
}

// TestMCPSendKeepsTheCallersDeadline pins the other half of the rule in send: a
// caller that supplied a deadline gets that one, not the default.
func TestMCPSendKeepsTheCallersDeadline(t *testing.T) {
	previous := defaultRequestTimeout
	defaultRequestTimeout = time.Hour
	t.Cleanup(func() { defaultRequestTimeout = previous })

	client := startFrameServer(t, func(req string) string {
		if strings.Contains(req, `"method":"initialize"`) {
			return `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"silent","version":"1"}}}`
		}
		return ""
	})
	if _, err := client.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.CallTool(ctx, "echo", nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("CallTool with an expiring context returned success")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want the caller's deadline", err)
	}
	if strings.Contains(err.Error(), "no response within") {
		t.Errorf("the default bound replaced the caller's deadline: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("CallTool took %s, want the caller's 100 ms deadline to win", elapsed)
	}
}

// TestAwaitResponsePrefersADeliveredResponse pins the ordering rule of issue #39:
// readLoop dispatches a frame before it marks the client closed, so a response
// that is already in the channel must be returned even when the transport died in
// the same instant. The iteration count is the point — a select over two ready
// channels is random, so a single case would only flake while this fails about
// half the time without the drain.
func TestAwaitResponsePrefersADeliveredResponse(t *testing.T) {
	client := NewClient(io.Discard, strings.NewReader(""), nil)
	client.markClosed(errors.New("mcp reader stopped: read: EOF"))

	frame := json.RawMessage(`{"jsonrpc":"2.0","id":7,"result":{"contents":[{"text":"kept"}]}}`)
	const want = `{"contents":[{"text":"kept"}]}`
	for i := 0; i < 1000; i++ {
		ch := make(chan json.RawMessage, 1)
		ch <- frame

		raw, err := client.awaitResponse(context.Background(), "resources/read", ch)
		if err != nil {
			t.Fatalf("iteration %d: awaitResponse = %v, want the delivered response", i, err)
		}
		if string(raw) != want {
			t.Fatalf("iteration %d: result = %s, want %s", i, raw, want)
		}
	}

	// A request whose channel was closed instead of answered (the flood budget)
	// must still fail rather than wait for a response that will never come.
	empty := make(chan json.RawMessage)
	close(empty)
	if _, err := client.awaitResponse(context.Background(), "resources/read", empty); err == nil {
		t.Error("awaitResponse returned success for a closed response channel")
	}
}
