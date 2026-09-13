package tool

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/config"
)

// TestHelperMCPProcess is a fake MCP server used as the stdio child of
// TestConnectMCPStdioKeepsChildAlive. It runs only when spawned by that test,
// which sets TINYCODE_MCP_HELPER in the environment.
func TestHelperMCPProcess(t *testing.T) {
	if os.Getenv("TINYCODE_MCP_HELPER") != "1" {
		t.Skip("helper process for stdio MCP tests")
	}
	serveFakeMCP(os.Stdin, os.Stdout)
	os.Exit(0)
}

// serveFakeMCP answers a minimal subset of the MCP protocol on the given
// streams until the peer closes them.
func serveFakeMCP(in io.Reader, out io.Writer) {
	reader := bufio.NewReader(in)
	for {
		body, err := readFramedMessage(reader)
		if err != nil {
			return
		}

		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			return
		}
		if req.ID == 0 {
			continue // notification: no reply
		}

		var result string
		switch req.Method {
		case "initialize":
			result = `{"serverInfo":{"name":"fake","version":"1.0.0"}}`
		case "tools/list":
			result = `{"tools":[{"name":"echo","description":"Echo text","inputSchema":{"type":"object","properties":{"text":{"type":"string"}}}}]}`
		case "tools/call":
			var params struct {
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			text, _ := params.Arguments["text"].(string)
			payload, _ := json.Marshal(map[string]any{
				"content": []map[string]string{{"type": "text", "text": "echo:" + text}},
			})
			result = string(payload)
		case "resources/list":
			result = `{"resources":[]}`
		default:
			_ = writeFramedMessage(out, fmt.Sprintf(
				`{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"method not found"}}`, req.ID))
			continue
		}

		if err := writeFramedMessage(out, fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result)); err != nil {
			return
		}
	}
}

func readFramedMessage(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
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
			return nil, err
		}
		length = n
	}
	if length < 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

func writeFramedMessage(w io.Writer, body string) error {
	_, err := io.WriteString(w, fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body))
	return err
}

// TestConnectMCPStdioKeepsChildAlive reproduces the bug where the handshake
// timeout context was cancelled as soon as connectMCP returned: the stdio
// child was killed and every later tool call failed even though the tool list
// was still advertised.
func TestConnectMCPStdioKeepsChildAlive(t *testing.T) {
	t.Setenv("TINYCODE_MCP_HELPER", "1")

	cfg := config.MCPServerConfig{
		Name:      "fake",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestHelperMCPProcess"},
	}

	tools, err := ConnectMCPServers(context.Background(), []config.MCPServerConfig{cfg})
	if err != nil {
		t.Fatalf("ConnectMCPServers: %v", err)
	}
	defer CloseMCPServers()

	echo := -1
	for i := range tools {
		if tools[i].Name == "mcp_fake_echo" {
			echo = i
			break
		}
	}
	if echo < 0 {
		names := make([]string, 0, len(tools))
		for _, tl := range tools {
			names = append(names, tl.Name)
		}
		t.Fatalf("expected tool %q, got %v", "mcp_fake_echo", names)
	}

	// Give the old implementation's deferred cancel time to fire; the child
	// must survive it.
	time.Sleep(300 * time.Millisecond)

	out, err := tools[echo].Execute(context.Background(), map[string]any{"text": "one"})
	if err != nil {
		t.Fatalf("first call after connect: %v", err)
	}
	if out != "echo:one" {
		t.Fatalf("first call: expected %q, got %q", "echo:one", out)
	}

	out, err = tools[echo].Execute(context.Background(), map[string]any{"text": "two"})
	if err != nil {
		t.Fatalf("second call after connect: %v", err)
	}
	if out != "echo:two" {
		t.Fatalf("second call: expected %q, got %q", "echo:two", out)
	}
}

// TestConnectMCPStdioHandshakeTimeoutKillsChild ensures a child that never
// answers the handshake is killed and reaped by the timeout instead of
// blocking forever or lingering as a zombie.
func TestConnectMCPStdioHandshakeTimeoutKillsChild(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep command not available")
	}

	cfg := config.MCPServerConfig{
		Name:      "silent",
		Transport: "stdio",
		Command:   sleep,
		Args:      []string{"30"},
	}

	start := time.Now()
	client, err := connectMCP(context.Background(), &cfg, 200*time.Millisecond)
	if err == nil {
		client.Client.Close()
		t.Fatal("expected handshake timeout error, got nil")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("handshake timeout was not enforced; took %v", elapsed)
	}
}

// TestHelperMCPStderrBlocker writes far more to stderr than a pipe buffer holds
// and then exits without answering the handshake. Without a drain the child
// blocks on the write and the parent only gives up at the handshake timeout.
func TestHelperMCPStderrBlocker(t *testing.T) {
	if os.Getenv("TINYCODE_MCP_STDERR_HELPER") != "1" {
		t.Skip("helper process for MCP stderr tests")
	}
	line := strings.Repeat("x", 1023) + "\n"
	for i := 0; i < 2048; i++ { // ~2 MiB, well past any pipe buffer
		fmt.Fprint(os.Stderr, "MCP-STDERR-MARKER ", line)
	}
	os.Exit(3)
}

// TestConnectMCPStdioDrainsStderr verifies that server stderr is drained (so the
// child cannot block) and that the captured output reaches the error message.
func TestConnectMCPStdioDrainsStderr(t *testing.T) {
	t.Setenv("TINYCODE_MCP_STDERR_HELPER", "1")

	cfg := &config.MCPServerConfig{
		Name:      "noisy",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestHelperMCPStderrBlocker"},
	}

	start := time.Now()
	_, err := connectMCP(context.Background(), cfg, 30*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the handshake to fail for a child that never answers")
	}
	if elapsed > 15*time.Second {
		t.Errorf("handshake took %s; the child most likely blocked writing stderr", elapsed)
	}
	if !strings.Contains(err.Error(), "MCP-STDERR-MARKER") {
		t.Errorf("server stderr excerpt missing from the error: %v", err)
	}
}

// TestHelperMCPPidProcess is a fake MCP server that records its own pid before
// serving, so TestCloseMCPServersReapsChildAndIsIdempotent can verify the child
// is reaped by shutdown.
func TestHelperMCPPidProcess(t *testing.T) {
	if os.Getenv("TINYCODE_MCP_PID_HELPER") != "1" {
		t.Skip("helper process for MCP shutdown tests")
	}
	if path := os.Getenv("TINYCODE_MCP_PID_FILE"); path != "" {
		if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(4)
		}
	}
	serveFakeMCP(os.Stdin, os.Stdout)
	os.Exit(0)
}

// TestCloseMCPServersReapsChildAndIsIdempotent verifies the exported shutdown
// hook closes stdio clients, leaves no child process behind, and is safe to
// call more than once.
func TestCloseMCPServersReapsChildAndIsIdempotent(t *testing.T) {
	t.Setenv("TINYCODE_MCP_PID_HELPER", "1")
	pidFile := filepath.Join(t.TempDir(), "mcp-child.pid")
	t.Setenv("TINYCODE_MCP_PID_FILE", pidFile)

	cfg := config.MCPServerConfig{
		Name:      "pidfake",
		Transport: "stdio",
		Command:   os.Args[0],
		Args:      []string{"-test.run=TestHelperMCPPidProcess"},
	}

	tools, err := ConnectMCPServers(context.Background(), []config.MCPServerConfig{cfg})
	if err != nil {
		t.Fatalf("ConnectMCPServers: %v", err)
	}

	echo := -1
	for i := range tools {
		if tools[i].Name == "mcp_pidfake_echo" {
			echo = i
			break
		}
	}
	if echo < 0 {
		t.Fatalf("expected tool %q, got %d tools", "mcp_pidfake_echo", len(tools))
	}

	// The child works before shutdown.
	out, err := tools[echo].Execute(context.Background(), map[string]any{"text": "before-close"})
	if err != nil {
		t.Fatalf("tool call before shutdown: %v", err)
	}
	if out != "echo:before-close" {
		t.Fatalf("expected %q, got %q", "echo:before-close", out)
	}

	pid := readHelperPID(t, pidFile)
	if err := signalProcess(pid); err != nil {
		t.Fatalf("child %d is not alive before shutdown: %v", pid, err)
	}

	CloseMCPServers()
	CloseMCPServers() // must be a safe no-op

	deadline := time.Now().Add(5 * time.Second)
	for signalProcess(pid) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("child process %d survived CloseMCPServers", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// readHelperPID waits for the helper child to publish its pid.
func readHelperPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if convErr == nil && pid > 0 {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper pid file %s was not written", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// signalProcess reports whether the process still exists by sending signal 0.
func signalProcess(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.Signal(0))
}
