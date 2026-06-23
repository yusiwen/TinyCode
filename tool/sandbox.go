package tool

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/tlog"
)

// AccessDenied is returned when a file access violates the sandbox policy.
type AccessDenied struct {
	Path    string
	Message string
}

func (e *AccessDenied) Error() string {
	return e.Message
}

// DenyHint returns a prompt message for display.
func (e *AccessDenied) DenyHint() string {
	return fmt.Sprintf(`[SECURITY] %s

To allow access, tell me one of:
  - "allow %s" — permit this one time
  - "always %s" — permit for this entire session
  - "deny %s" — block this access`, e.Message, e.Path, e.Path, e.Path)
}

// ── Pattern D: Permission Caching & Auto-approve ──

// SandboxConfig holds security configuration for tool execution.
type SandboxConfig struct {
	ProjectRoot string   // allowed directory root
	DenyCommands []string // command deny list (kept for backward compat)

	// AutoAllowPaths are paths automatically allowed (CWD, parent, etc.)
	AutoAllowPaths []string

	mu     sync.Mutex
	allowedPaths map[string]bool
}

var DefaultSandbox = &SandboxConfig{
	DenyCommands: []string{
		"rm -rf /", "rm -rf /*", "rm -rf --no-preserve-root",
		"sudo ", "su ", "chmod -R /", "chown -R /",
		"dd if=", "mkfs.", "fdisk", "mkswap",
		"shutdown", "reboot", "init 0", "halt",
		"> /dev/sd", "< /dev/sd", "mkfs",
		"pvcreate", "vgcreate", "lvcreate",
		":(){ :|:& };:", // fork bomb
	},
	allowedPaths: make(map[string]bool),
}

func (sc *SandboxConfig) CheckCommand(cmd string) error {
	cmdLower := strings.ToLower(strings.TrimSpace(cmd))
	for _, deny := range sc.DenyCommands {
		if strings.Contains(cmdLower, strings.ToLower(deny)) {
			tlog.Warn("sandbox", "cmd_blocked", "pattern", deny, "cmd", cmd)
			return fmt.Errorf("command blocked by security policy (matches: %q)", deny)
		}
	}
	return nil
}

// CheckPath checks if the given path is allowed. Returns nil if allowed,
// or an *AccessDenied error. Pattern D auto-rules are checked before rejection.
func (sc *SandboxConfig) CheckPath(absPath string) error {
	if sc.ProjectRoot == "" {
		return nil
	}

	abs, err := filepath.Abs(absPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	root, err := filepath.Abs(sc.ProjectRoot)
	if err != nil {
		return fmt.Errorf("resolve root: %w", err)
	}

	// 1) Within project root
	rel, err := filepath.Rel(root, abs)
	if err == nil && !strings.HasPrefix(rel, "..") {
		return nil
	}

	// 2) Cached allow
	sc.mu.Lock()
	allowed := sc.allowedPaths[abs]
	sc.mu.Unlock()
	if allowed {
		return nil
	}

	// 3) Pattern D: auto-allow paths (CWD, parent dir, etc.)
	for _, permit := range sc.AutoAllowPaths {
		permitAbs, err := filepath.Abs(permit)
		if err != nil {
			continue
		}
		pRel, err := filepath.Rel(permitAbs, abs)
		if err == nil && !strings.HasPrefix(pRel, "..") {
			// Auto-cache so future checks are instant
			sc.AllowAlways(abs)
			return nil
		}
	}

	return &AccessDenied{
		Path:    abs,
		Message: fmt.Sprintf("File %q is outside the project root %q.", abs, root),
	}
}

func (sc *SandboxConfig) AllowOnce(absPath string) {
	// One-time permission: do NOT add to allowedPaths.
	// The next call for the same path will trigger the permission dialog again.
}

func (sc *SandboxConfig) AllowSession(absPath string) {
	abs, _ := filepath.Abs(absPath)
	sc.mu.Lock()
	sc.allowedPaths[abs] = true
	sc.mu.Unlock()
}

func (sc *SandboxConfig) AllowAlways(absPath string) {
	abs, _ := filepath.Abs(absPath)
	sc.mu.Lock()
	sc.allowedPaths[abs] = true
	sc.mu.Unlock()
}

func (sc *SandboxConfig) ResetAllowed() {
	sc.mu.Lock()
	sc.allowedPaths = make(map[string]bool)
	sc.mu.Unlock()
}

// ── Pattern C: Interactive Permission Queue ──

// pendingPerm holds the current outstanding permission request.
var (
	pendingPerm   *PermissionRequest
	pendingMu     sync.Mutex
)

// PermissionRequest is queued when a path needs user approval.
type PermissionRequest struct {
	Path       string
	AgentLabel string // who is asking (e.g. "build", "general")
	Allowed    bool   // set to true by TUI when user approves
	Mode       string // "once" or "always"
}

// currentAgentLabel tracks which agent is the current caller for permission requests.
var currentAgentLabel string
var currentAgentMu sync.Mutex

// SetAgentLabel sets the label for the currently active agent (set by task tool).
func SetAgentLabel(label string) {
	currentAgentMu.Lock()
	currentAgentLabel = label
	currentAgentMu.Unlock()
}

// RequestPermission queues a path for user approval and blocks the
// calling goroutine until the user responds via ResolvePermission.
func RequestPermission(ctx context.Context, path string) (bool, string) {
	currentAgentMu.Lock()
	label := currentAgentLabel
	currentAgentMu.Unlock()

	pendingMu.Lock()
	pendingPerm = &PermissionRequest{Path: path, AgentLabel: label, Allowed: false}
	pendingMu.Unlock()

	tlog.Info("sandbox", "permission_request", "path", path)

	// Block until resolved or context cancelled
	for {
		select {
		case <-ctx.Done():
			pendingMu.Lock()
			pendingPerm = nil
			pendingMu.Unlock()
			return false, "cancelled"
		default:
			pendingMu.Lock()
			r := pendingPerm
			allowed := r != nil && r.Allowed
			denied := r != nil && r.Mode == "denied"
			mode := ""
			if r != nil {
				mode = r.Mode
			}
			pendingMu.Unlock()
			if allowed || denied || r == nil {
				return allowed, mode
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// HasPendingPermission returns true if a permission request is waiting.
func HasPendingPermission() bool {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	return pendingPerm != nil && !pendingPerm.Allowed
}

// PendingPermissionPath returns the path of the pending request, if any.
func PendingPermissionPath() string {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if pendingPerm != nil {
		return pendingPerm.Path
	}
	return ""
}

// PendingPermissionAgentLabel returns the agent label of the pending request.
func PendingPermissionAgentLabel() string {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if pendingPerm != nil {
		return pendingPerm.AgentLabel
	}
	return ""
}

// ResolvePermission approves or denies a pending permission request.
// Called from TUI when user types "allow" or "deny".
func ResolvePermission(path string, allow bool, mode string) bool {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if pendingPerm == nil {
		return false // no pending request
	}
	if path != "" && pendingPerm.Path != path {
		return false // path mismatch
	}
	pendingPerm.Allowed = allow
	pendingPerm.Mode = mode
	if allow {
		switch mode {
		case "once":
			DefaultSandbox.AllowOnce(pendingPerm.Path)
		case "always":
			DefaultSandbox.AllowAlways(pendingPerm.Path)
		default:
			DefaultSandbox.AllowSession(pendingPerm.Path)
		}
	}
	return true
}

// CancelPendingPermission cancels a pending request (e.g., on interrupt).
func CancelPendingPermission() {
	pendingMu.Lock()
	pendingPerm = nil
	pendingMu.Unlock()
}

// SandboxAllowTool returns an agent.Tool that lets the LLM or TUI
// approve a blocked path.
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
					"type":        "string",
					"enum":        []string{"once", "always"},
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
			// Also resolve any pending permission request
			ResolvePermission(p, true, mode)
			return fmt.Sprintf("Path %s has been allowed (%s). You may retry the operation.", p, mode), nil
		},
	}
}
