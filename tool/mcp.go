package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/mcp"
	"github.com/yusiwen/tinycode/tlog"
)

// mcpClient wraps an MCP client with its server name.
type mcpClient struct {
	ServerName string
	Client     mcp.MCPClient
	Tools      []mcp.Tool
}

// ConnectMCPServers connects to all configured MCP servers and discovers their tools.
// Returns a list of wrapped agent.Tools ready for registration.
func ConnectMCPServers(servers []config.MCPServerConfig) ([]agent.Tool, error) {
	if len(servers) == 0 {
		return nil, nil
	}

	var allTools []agent.Tool
	timeout := 60 * time.Second

	for _, s := range servers {
		tlog.Info("tool.mcp", "connecting", "server", s.Name, "transport", s.Transport)
		client, err := connectMCP(context.Background(), &s, timeout)
		if err != nil {
			tlog.Warn("tool.mcp", "connect failed",
				"server", s.Name,
				"error", err.Error())
			continue
		}

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
					result, err := client.Client.CallTool(mt.Name, args)
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
	}

	return allTools, nil
}

// connectMCP connects to a single MCP server, initializes, and discovers tools.
func connectMCP(ctx context.Context, cfg *config.MCPServerConfig, timeout time.Duration) (*mcpClient, error) {
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch cfg.Transport {
	case "stdio":
		return connectMCPStdio(ctx2, cfg)
	case "http":
		return connectMCPHTTP(ctx2, cfg)
	default:
		return nil, fmt.Errorf("unknown MCP transport: %s", cfg.Transport)
	}
}

// connectMCPStdio connects via a subprocess over stdin/stdout.
func connectMCPStdio(ctx context.Context, cfg *config.MCPServerConfig) (*mcpClient, error) {
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", cfg.Command, err)
	}

	client := mcp.NewClient(stdin, stdout, stderr)

	// Initialize
	if _, err := client.Initialize(); err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("initialize %s: %w", cfg.Name, err)
	}

	// Discover tools
	tools, err := client.ListTools()
	if err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("list tools %s: %w", cfg.Name, err)
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
func connectMCPHTTP(ctx context.Context, cfg *config.MCPServerConfig) (*mcpClient, error) {
	// SSRF check
	if err := checkMCPURL(cfg.URL); err != nil {
		return nil, err
	}
	client := mcp.NewHTTPClient(cfg.URL, cfg.Headers)

	if _, err := client.Initialize(); err != nil {
		return nil, fmt.Errorf("initialize %s: %w", cfg.Name, err)
	}
	tools, err := client.ListTools()
	if err != nil {
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

	ips, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("MCP URL DNS lookup failed for %q: %w", host, err)
	}
	for _, ipStr := range ips {
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
			return fmt.Errorf("MCP URL blocked: private IP %q for %q", ipStr, rawURL)
		}
	}
	return nil
}
