package lsp

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Server manages the lifecycle of a single LSP server process.
type Server struct {
	cmd    *exec.Cmd
	Client *Client
	Conn   *Conn
}

// Start launches the LSP server process and establishes a connection.
// command is the LSP binary (e.g. "gopls"), args are any additional arguments.
func Start(ctx context.Context, command string, args ...string) (*Server, error) {
	cmd := exec.CommandContext(ctx, command, args...)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	// Connect stderr to /dev/null (LSP servers log diagnostics to stderr)
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", command, err)
	}

	conn := NewConn(stdin, stdout)
	client := NewClient(conn)

	return &Server{
		cmd:    cmd,
		Client: client,
		Conn:   conn,
	}, nil
}

// Close shuts down the LSP server gracefully.
func (s *Server) Close() error {
	s.Client.Shutdown()
	s.Conn.Close()
	if s.cmd.Process != nil {
		return s.cmd.Process.Kill()
	}
	return nil
}

// Config holds the mapping from language to LSP server command.
type Config struct {
	Language string
	Command  string
	Args     []string
}

// DefaultConfigs is the built-in mapping of languages to LSP servers.
// Users can override this via .env or config file.
var DefaultConfigs = []Config{
	{Language: "go",         Command: "gopls"},
	{Language: "python",     Command: "pyright",     Args: []string{"--stdio"}},
	{Language: "typescript", Command: "typescript-language-server", Args: []string{"--stdio"}},
	{Language: "javascript", Command: "typescript-language-server", Args: []string{"--stdio"}},
	{Language: "rust",       Command: "rust-analyzer"},
	{Language: "cpp",        Command: "clangd"},
	{Language: "java",       Command: "java",         Args: []string{"-jar", "eclipse.jdt.ls"}},
}

// FindConfig looks up the LSP server config for a language.
// Returns nil if no config is found.
func FindConfig(language string) *Config {
	for _, c := range DefaultConfigs {
		if c.Language == language {
			return &c
		}
	}
	return nil
}

// DetectLanguage attempts to detect the programming language of a project
// based on files in its root directory. Returns "go", "python", etc.
// Returns empty string if unable to detect.
func DetectLanguage(rootDir string) string {
	// Use simple file existence checks via `ls` or `test`
	checks := []struct {
		file     string
		language string
	}{
		{"go.mod", "go"},
		{"go.sum", "go"},
		{"pyproject.toml", "python"},
		{"setup.py", "python"},
		{"requirements.txt", "python"},
		{"package.json", "typescript"},
		{"tsconfig.json", "typescript"},
		{"Cargo.toml", "rust"},
		{"CMakeLists.txt", "cpp"},
		{"pom.xml", "java"},
		{"build.gradle", "java"},
	}

	for _, check := range checks {
		cmd := exec.Command("test", "-f", rootDir+"/"+check.file)
		if cmd.Run() == nil {
			return check.language
		}
	}

	// Fallback: look at file extensions
	cmd := exec.Command("sh", "-c",
		fmt.Sprintf("ls %s/*.go 2>/dev/null | head -1", rootDir))
	if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		return "go"
	}

	cmd = exec.Command("sh", "-c",
		fmt.Sprintf("ls %s/*.py 2>/dev/null | head -1", rootDir))
	if out, err := cmd.Output(); err == nil && strings.TrimSpace(string(out)) != "" {
		return "python"
	}

	return ""
}
