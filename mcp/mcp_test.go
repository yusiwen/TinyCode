package mcp

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
)

// mockServer runs a minimal MCP server over pipes and returns a connected client.
// cancel() stops the goroutine.
func startMockServer(t *testing.T) (*Client, context.CancelFunc) {
	t.Helper()

	clientStdinR, clientStdinW := io.Pipe()     // client → server
	serverStdoutR, serverStdoutW := io.Pipe()   // server → client

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
	var contentLength int
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1)
	for {
		n, err := r.Read(tmp)
		if err != nil {
			t.Fatalf("read: %v", err)
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
	io.ReadFull(r, body)
	return string(body)
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

	info, err := client.Initialize()
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

	if _, err := client.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	tools, err := client.ListTools()
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

	if _, err := client.Initialize(); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if _, err := client.ListTools(); err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	result, err := client.CallTool("echo", map[string]any{"text": "hello"})
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

	_, err := client.Initialize()
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
