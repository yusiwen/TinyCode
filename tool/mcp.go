package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/mcp"
	"github.com/yusiwen/tinycode/tlog"
)

// mcpClients tracks every client created by ConnectMCPServers so a single call
// to CloseMCPServers can release them all at shutdown. mcpClientsMu guards the
// slice; it is small and only touched on connect and at shutdown.
var (
	mcpClientsMu sync.Mutex
	mcpClients   []mcp.MCPClient
)

// registerMCPClient records a connected client for CloseMCPServers.
func registerMCPClient(c mcp.MCPClient) {
	mcpClientsMu.Lock()
	mcpClients = append(mcpClients, c)
	mcpClientsMu.Unlock()
}

// CloseMCPServers closes every MCP client created by ConnectMCPServers and
// reaps their stdio children. It is safe to call more than once and from any
// goroutine: the tracked list is cleared before closing, and each client Close
// is itself idempotent, so a second call is a no-op.
//
// Call it once from main when the agent is done, for example on shutdown.
func CloseMCPServers() {
	mcpClientsMu.Lock()
	clients := mcpClients
	mcpClients = nil
	mcpClientsMu.Unlock()

	for _, c := range clients {
		if err := c.Close(); err != nil {
			tlog.Warn("tool.mcp", "close failed", "error", err.Error())
		}
	}
}

// mcpStderrCapture bounds how much of a server's stderr is kept for diagnostics
// (the pipe is always drained; only the retained copy is capped).
const mcpStderrCapture = 8 * 1024

// stderrSuffix renders captured server stderr for an error message.
func stderrSuffix(buf *limitedBuffer) string {
	if buf == nil || buf.Len() == 0 {
		return ""
	}
	text := strings.TrimSpace(buf.String())
	if text == "" {
		return ""
	}
	if buf.truncated {
		text += "\n[stderr truncated]"
	}
	return "\nserver stderr:\n" + text
}

// mcpClient wraps an MCP client with its server name.
type mcpClient struct {
	ServerName string
	Client     mcp.MCPClient
	Tools      []mcp.Tool
}

// ConnectMCPServers connects to all configured MCP servers and discovers their tools.
// Returns a list of wrapped agent.Tools ready for registration.
func ConnectMCPServers(ctx context.Context, servers []config.MCPServerConfig) ([]agent.Tool, error) {
	if len(servers) == 0 {
		return nil, nil
	}

	var allTools []agent.Tool
	timeout := 60 * time.Second

	for _, s := range servers {
		tlog.Info("tool.mcp", "connecting", "server", s.Name, "transport", s.Transport)
		client, err := connectMCP(ctx, &s, timeout)
		if err != nil {
			tlog.Warn("tool.mcp", "connect failed",
				"server", s.Name,
				"error", err.Error())
			continue
		}

		// Track the client so CloseMCPServers can release it (and reap the
		// stdio child) at shutdown.
		registerMCPClient(client.Client)

		for _, mt := range client.Tools {
			name := fmt.Sprintf("mcp_%s_%s", s.Name, mt.Name)
			desc := mt.Description
			if desc == "" {
				desc = fmt.Sprintf("MCP tool from %s", s.Name)
			}

			// Parse input schema
			params := parseMCPSchema(mt.InputSchema)
			if params == nil {
				params = map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"additionalProperties": true,
				}
			}

			t := agent.Tool{
				Name:        name,
				Description: desc,
				Parameters:  params,
				Execute: func(ctx context.Context, args map[string]any) (string, error) {
					result, err := client.Client.CallTool(ctx, mt.Name, args)
					if err != nil {
						return "", fmt.Errorf("mcp %s: %w", name, err)
					}
					if result.IsError {
						msg := "unknown error"
						if len(result.Content) > 0 {
							msg = result.Content[0].Text
						}
						return "", fmt.Errorf("mcp %s error: %s", name, msg)
					}
					var parts []string
					for _, c := range result.Content {
						if c.Text != "" {
							parts = append(parts, c.Text)
						}
					}
					return strings.Join(parts, "\n"), nil
				},
			}
			allTools = append(allTools, t)
		}

		// Register resource tools for this server
		resources, err := client.Client.ListResources(ctx)
		if err == nil && len(resources) > 0 {
			resName := fmt.Sprintf("mcp_%s_list_resources", s.Name)
			allTools = append(allTools, agent.Tool{
				Name:        resName,
				Description: fmt.Sprintf("List available resources from MCP server %s", s.Name),
				Parameters: map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
				Execute: func(ctx context.Context, args map[string]any) (string, error) {
					resources, err := client.Client.ListResources(ctx)
					if err != nil {
						return "", fmt.Errorf("mcp list resources: %w", err)
					}
					if len(resources) == 0 {
						return "No resources available.", nil
					}
					var sb strings.Builder
					sb.WriteString(fmt.Sprintf("%d resources from %s:\n", len(resources), s.Name))
					for _, r := range resources {
						sb.WriteString(fmt.Sprintf("  %s: %s (%s)\n", r.URI, r.Name, r.Description))
					}
					return strings.TrimSpace(sb.String()), nil
				},
			})
		}
	}

	return allTools, nil
}

// connectMCP connects to a single MCP server, initializes, and discovers tools.
// timeout bounds the handshake only; for stdio transports the child process
// lives on after this function returns until the client is closed.
func connectMCP(ctx context.Context, cfg *config.MCPServerConfig, timeout time.Duration) (*mcpClient, error) {
	switch cfg.Transport {
	case "stdio":
		return connectMCPStdio(ctx, cfg, timeout)
	case "http":
		return connectMCPHTTP(ctx, cfg, timeout)
	default:
		return nil, fmt.Errorf("unknown MCP transport: %s", cfg.Transport)
	}
}

// connectMCPStdio connects via a subprocess over stdin/stdout.
//
// The child is started from a context that is cancelled only when the client
// is closed (or when the handshake fails). The handshake timeout is applied to
// the initialize/list requests alone, so a successful connect leaves the
// subprocess alive for every later CallTool.
func connectMCPStdio(ctx context.Context, cfg *config.MCPServerConfig, timeout time.Duration) (*mcpClient, error) {
	procCtx, procCancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, cfg.Command, cfg.Args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		procCancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		procCancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		procCancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		procCancel()
		return nil, fmt.Errorf("start %s: %w", cfg.Command, err)
	}

	// Reap the child exactly once. Killing it through procCtx unblocks Wait, so
	// the process never lingers as a zombie.
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()

	// Drain stderr for the lifetime of the child. An unread pipe fills up and
	// blocks the server, and the captured output is what makes a failed
	// handshake diagnosable.
	stderrTail := &limitedBuffer{limit: mcpStderrCapture}
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		_, _ = io.Copy(stderrTail, stderr)
	}()

	client := mcp.NewClient(stdin, stdout, stderr)
	client.SetKillFunc(func() {
		procCancel()
		<-reaped
		<-stderrDone // the pipe closes when the process is gone
	})

	// The handshake gets its own bounded context; procCtx above is independent
	// of it so the process survives after connectMCPStdio returns.
	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, timeout)
	defer cancelHandshake()

	if _, err := client.Initialize(handshakeCtx); err != nil {
		_ = client.Close() // kill and reap the child on handshake failure
		return nil, fmt.Errorf("initialize %s: %w%s", cfg.Name, err, stderrSuffix(stderrTail))
	}

	tools, err := client.ListTools(handshakeCtx)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("list tools %s: %w%s", cfg.Name, err, stderrSuffix(stderrTail))
	}

	tlog.Info("tool.mcp", "connected",
		"server", cfg.Name,
		"tools", len(tools))

	return &mcpClient{
		ServerName: cfg.Name,
		Client:     client,
		Tools:      tools,
	}, nil
}

// parseMCPSchema converts an MCP tool's inputSchema JSON to a Go map suitable
// for use as agent.Tool.Parameters.
func parseMCPSchema(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil
	}
	return schema
}

// connectMCPHTTP connects via HTTP POST to a remote MCP endpoint.
// timeout bounds the initialize/list handshake; later calls use the caller's
// context plus the HTTP client's own timeout.
func connectMCPHTTP(ctx context.Context, cfg *config.MCPServerConfig, timeout time.Duration) (*mcpClient, error) {
	// SSRF check
	if err := checkMCPURL(cfg.URL); err != nil {
		return nil, err
	}
	client := mcp.NewHTTPClient(cfg.URL, cfg.Headers)

	handshakeCtx, cancelHandshake := context.WithTimeout(ctx, timeout)
	defer cancelHandshake()

	if _, err := client.Initialize(handshakeCtx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("initialize %s: %w", cfg.Name, err)
	}
	tools, err := client.ListTools(handshakeCtx)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("list tools %s: %w", cfg.Name, err)
	}

	tlog.Info("tool.mcp", "connected (HTTP)",
		"server", cfg.Name,
		"url", cfg.URL,
		"tools", len(tools))

	return &mcpClient{
		ServerName: cfg.Name,
		Client:     client,
		Tools:      tools,
	}, nil
}

// checkMCPURL validates an MCP HTTP endpoint URL for SSRF safety.
// Only allows http/https to non-private IPs.
func checkMCPURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid MCP URL %q: %w", rawURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("MCP URL scheme must be http or https, got %q", u.Scheme)
	}

	host := u.Hostname()
	// Quick check: localhost is allowed for MCP (common dev pattern)
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	// Wildcard/unspecified addresses (0.0.0.0, ::) bind every interface and must
	// never be treated as a valid remote endpoint.
	if ip := net.ParseIP(host); ip != nil && ip.IsUnspecified() {
		return fmt.Errorf("MCP URL blocked: unspecified address %q", host)
	}

	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("MCP URL DNS lookup failed for %q: %w", host, err)
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("MCP URL blocked: non-public IP %q for %q", ipStr, rawURL)
		}
	}
	return nil
}
