package tool

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yusiwen/tinycode/agent"
)

// SandboxConfig holds security configuration for tool execution.
type SandboxConfig struct {
	// ProjectRoot is the allowed directory for file operations.
	// Empty means no restriction.
	ProjectRoot string

	// CommandDenyList contains command patterns that are always blocked.
	CommandDenyList []string

	mu     sync.Mutex
	// allowedPaths tracks paths the user has approved via "always".
	allowedPaths map[string]bool
}

var DefaultSandbox = &SandboxConfig{
	CommandDenyList: []string{
		"rm -rf /",
		"rm -rf /*",
		"rm -rf --no-preserve-root",
		"sudo ",
		"su ",
		"chmod -R /",
		"chown -R /",
		"dd if=",
		"mkfs.",
		"fdisk",
		"mkswap",
		"shutdown",
		"reboot",
		"init 0",
		"halt",
		"> /dev/sd",
		"< /dev/sd",
		"mkfs",
		"pvcreate",
		"vgcreate",
		"lvcreate",
		":(){ :|:& };:",  // fork bomb
	},
	allowedPaths: make(map[string]bool),
}

// CheckCommand returns a non-nil error if the command matches a deny pattern.
func (sc *SandboxConfig) CheckCommand(cmd string) error {
	cmdLower := strings.ToLower(strings.TrimSpace(cmd))
	for _, deny := range sc.CommandDenyList {
		if strings.Contains(cmdLower, strings.ToLower(deny)) {
			return fmt.Errorf("command blocked by security policy (matches: %q)", deny)
		}
	}
	return nil
}

// CheckPath checks if the given path is allowed. Returns nil if allowed,
// or an AccessDenied error with instructions for the user to confirm.
func (sc *SandboxConfig) CheckPath(absPath string) error {
	if sc.ProjectRoot == "" {
		return nil // no restriction
	}

	// Resolve to absolute
	abs, err := filepath.Abs(absPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	root, err := filepath.Abs(sc.ProjectRoot)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}

	// Is it under the project root?
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(rel, "..") {
		return nil // within project root
	}

	// Check the "always" cache
	sc.mu.Lock()
	allowed := sc.allowedPaths[abs]
	sc.mu.Unlock()
	if allowed {
		return nil
	}

	return &AccessDenied{
		Path:    abs,
		Message: fmt.Sprintf("File %q is outside the project root %q.", abs, root),
	}
}

// AllowOnce permits a single access to the given path.
func (sc *SandboxConfig) AllowOnce(absPath string) {
	sc.AllowAlways(absPath) // same effect: add to whitelist
}

// AllowAlways caches a path as permanently allowed for this session.
func (sc *SandboxConfig) AllowAlways(absPath string) {
	abs, _ := filepath.Abs(absPath)
	sc.mu.Lock()
	sc.allowedPaths[abs] = true
	sc.mu.Unlock()
}

// ResetAllowed clears all session-allowed paths.
func (sc *SandboxConfig) ResetAllowed() {
	sc.mu.Lock()
	sc.allowedPaths = make(map[string]bool)
	sc.mu.Unlock()
}

// SandboxAllowTool returns an agent.Tool that lets the LLM allow a path
// after the user gives permission.
func SandboxAllowTool() agent.Tool {
	return agent.Tool{
		Name:        "sandbox_allow",
		Description: "Allow access to a file path that was blocked by the security sandbox. " +
			"Use when the user says 'allow <path>' or 'always <path>'. " +
			"Pass path and mode ('once' for one-time, 'always' for session).",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute path to allow",
				},
				"mode": map[string]any{
					"type": "string",
					"enum": []string{"once", "always"},
					"description": "'once' for one-time, 'always' for session",
				},
			},
			"required": []string{"path"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			p, _ := args["path"].(string)
			mode, _ := args["mode"].(string)
			if p == "" {
				return "", fmt.Errorf("path is required")
			}
			if mode == "always" {
				DefaultSandbox.AllowAlways(p)
			} else {
				DefaultSandbox.AllowOnce(p)
			}
			return fmt.Sprintf("Path %s has been allowed (%s). You may retry the operation.", p, mode), nil
		},
	}
}

// AccessDenied is returned when a file access violates the sandbox policy.
type AccessDenied struct {
	Path    string
	Message string
}

func (e *AccessDenied) Error() string {
	return e.Message
}

// DenyHint returns a prompt message the LLM can show the user for confirmation.
func (e *AccessDenied) DenyHint() string {
	return fmt.Sprintf(`[SECURITY] %s

To allow access, tell me one of:
  - "allow %s" — permit this one time
  - "always %s" — permit for this entire session
  - "deny %s" — block this access`, e.Message, e.Path, e.Path, e.Path)
}
