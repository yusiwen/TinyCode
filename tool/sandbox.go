package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

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
	ProjectRoot  string   // allowed directory root
	DenyCommands []string // command deny list (kept for backward compat)

	// AutoAllowPaths are paths automatically allowed (CWD, parent, etc.)
	AutoAllowPaths []string

	mu           sync.Mutex
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

// escapes reports whether a filepath.Rel result points outside its base.
func escapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// absoluteNoClean makes path absolute without collapsing "." or ".." the way
// filepath.Abs (which calls Clean) would. That matters for security: the OS
// resolves ".." against the already-resolved prefix, so "link/../outside" is
// NOT the same as the lexically cleaned path once "link" points elsewhere.
func absoluteNoClean(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	return wd + string(filepath.Separator) + path
}

// resolveRealPath returns path with symbolic links resolved the way the OS
// resolves them: every component is resolved in order, and ".." is applied to
// the already-resolved prefix. Components that do not exist yet are kept
// verbatim, so the result is usable for a path about to be created.
//
// This deliberately avoids filepath.Clean/filepath.Dir on the input, because
// those collapse "link/.." lexically and hide exactly the escape this function
// exists to expose.
func resolveRealPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}

	sep := string(filepath.Separator)
	prefix := path
	suffix := ""
	for {
		idx := strings.LastIndex(prefix, sep)
		if idx < 0 {
			return path
		}
		comp := prefix[idx+1:]
		if suffix == "" {
			suffix = comp
		} else {
			suffix = comp + sep + suffix
		}
		if idx == 0 {
			prefix = sep
		} else {
			prefix = prefix[:idx]
		}

		resolved, err := filepath.EvalSymlinks(prefix)
		if err != nil {
			if prefix == sep {
				return path
			}
			continue
		}
		if suffix == "" {
			return resolved
		}
		return resolved + sep + suffix
	}
}

// CheckPath checks if the given path is allowed. Returns nil if allowed,
// or an *AccessDenied error. Pattern D auto-rules are checked before rejection.
//
// Containment is decided on the OS-resolved forms of both the root and the
// requested path, so neither a symlink nor a "link/.." sequence can escape.
func (sc *SandboxConfig) CheckPath(absPath string) error {
	if sc.ProjectRoot == "" {
		return nil
	}

	rawAbs := absoluteNoClean(absPath)
	root := filepath.Clean(absoluteNoClean(sc.ProjectRoot))
	realAbs := filepath.Clean(resolveRealPath(rawAbs))
	realRoot := filepath.Clean(resolveRealPath(root))

	// 1) Within project root. The OS-resolved forms are authoritative: a
	// lexical check alone would accept a link (or a link plus "..") that
	// points outside.
	if rel, err := filepath.Rel(realRoot, realAbs); err == nil && !escapes(rel) {
		// On Linux the kernel re-evaluates the path with RESOLVE_BENEATH: if a
		// directory along it was swapped for an escaping symlink (or is a
		// magic link) after EvalSymlinks ran, the probe returns EXDEV and the
		// access is denied even though the resolved comparison passed.
		//
		// The probe must run on realAbs, the form the caller will actually
		// open (CheckPathAccess returns ResolvePath). Probing rawAbs would be
		// wrong: RESOLVE_BENEATH rejects absolute symlinks wherever they
		// point, so a symlink inside the root pointing back inside the root
		// would be reported as an escape even though it resolves here.
		if kernelEscapeCheck(root, realAbs) {
			return &AccessDenied{
				Path:    rawAbs,
				Message: fmt.Sprintf("Path %q escapes the project root %q.", rawAbs, sc.ProjectRoot),
			}
		}
		return nil
	}

	// 2) Cached allow (the requested and the resolved path may both be cached)
	sc.mu.Lock()
	allowed := sc.allowedPaths[rawAbs] || sc.allowedPaths[realAbs]
	sc.mu.Unlock()
	if allowed {
		return nil
	}

	// 3) Pattern D: auto-allow paths (CWD, parent dir, etc.), compared on the
	// resolved forms for the same reason.
	for _, permit := range sc.AutoAllowPaths {
		realPermit := filepath.Clean(resolveRealPath(absoluteNoClean(permit)))
		if pRel, err := filepath.Rel(realPermit, realAbs); err == nil && !escapes(pRel) {
			// Auto-cache so future checks are instant
			sc.AllowAlways(rawAbs)
			return nil
		}
	}

	return &AccessDenied{
		Path:    rawAbs,
		Message: fmt.Sprintf("File %q is outside the project root %q.", rawAbs, sc.ProjectRoot),
	}
}

// AllowOnce records a one-time approval. The caller that received the
// approval proceeds immediately; nothing is cached, so the next access to the
// same path asks again.
func (sc *SandboxConfig) AllowOnce(absPath string) {
	// Intentionally empty — see the method comment.
}

// allow stores a path in the session allow-list. Both the requested and the
// OS-resolved form are cached so later checks hit either variant.
func (sc *SandboxConfig) allow(absPath string) {
	rawAbs := absoluteNoClean(absPath)
	realAbs := filepath.Clean(resolveRealPath(rawAbs))
	sc.mu.Lock()
	sc.allowedPaths[rawAbs] = true
	sc.allowedPaths[realAbs] = true
	sc.mu.Unlock()
}

func (sc *SandboxConfig) AllowSession(absPath string) { sc.allow(absPath) }

func (sc *SandboxConfig) AllowAlways(absPath string) { sc.allow(absPath) }

func (sc *SandboxConfig) ResetAllowed() {
	sc.mu.Lock()
	sc.allowedPaths = make(map[string]bool)
	sc.mu.Unlock()
}

// ── Pattern C: Interactive Permission Queue ──

// PermissionRequest is queued when a path needs user approval.
// All fields except done are guarded by pendingMu; done is closed once the
// request is resolved and acts as the happens-before edge for the waiters.
type PermissionRequest struct {
	ID         uint64 // unique request id
	Path       string
	AgentLabel string // who is asking (e.g. "build", "general")
	Allowed    bool   // set to true by TUI when user approves
	Mode       string // "once", "session", "always", "denied", "cancelled"
	Resolved   bool   // set to true when TUI has responded (allow or deny)

	done chan struct{}
}

// pendingQueue holds every outstanding request in FIFO order. The TUI always
// answers the head; pendingPerm mirrors the head for display readers.
var (
	pendingMu    sync.Mutex
	pendingQueue []*PermissionRequest
	pendingPerm  *PermissionRequest
	permSeq      atomic.Uint64
)

// headLocked returns the request the user should answer. Caller holds pendingMu.
func headLocked() *PermissionRequest {
	if len(pendingQueue) == 0 {
		return nil
	}
	return pendingQueue[0]
}

// removeLocked drops req from the queue. Caller holds pendingMu.
func removeLocked(req *PermissionRequest) {
	for i, r := range pendingQueue {
		if r == req {
			pendingQueue = append(pendingQueue[:i], pendingQueue[i+1:]...)
			break
		}
	}
	pendingPerm = headLocked()
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
// calling goroutine until the user responds via ResolvePermission or ctx is
// cancelled. Every call owns its own request, so concurrent tool calls can
// never consume each other's approval.
func RequestPermission(ctx context.Context, path string) (bool, string) {
	currentAgentMu.Lock()
	label := currentAgentLabel
	currentAgentMu.Unlock()

	req := &PermissionRequest{
		ID:         permSeq.Add(1),
		Path:       path,
		AgentLabel: label,
		done:       make(chan struct{}),
	}

	pendingMu.Lock()
	pendingQueue = append(pendingQueue, req)
	if pendingPerm == nil {
		pendingPerm = req
	}
	pendingMu.Unlock()

	tlog.Info("sandbox", "permission_request", "path", path, "id", req.ID)

	select {
	case <-req.done:
	case <-ctx.Done():
		// Withdraw the request unless the user already resolved it.
		pendingMu.Lock()
		if !req.Resolved {
			req.Resolved = true
			req.Mode = "cancelled"
			removeLocked(req)
		}
		pendingMu.Unlock()
	}

	pendingMu.Lock()
	allowed, mode := req.Allowed, req.Mode
	pendingMu.Unlock()
	return allowed, mode
}

// HasPendingPermission returns true if a permission request is waiting.
func HasPendingPermission() bool {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	return pendingPerm != nil && !pendingPerm.Resolved
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

// PendingPermissionID returns the id of the queued request the user is being
// asked about, or 0 when nothing is pending. Callers that display a dialog must
// capture this id and answer with ResolvePermissionByID, so the answer always
// applies to the request that was shown.
func PendingPermissionID() uint64 {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	if pendingPerm != nil {
		return pendingPerm.ID
	}
	return 0
}

// resolveLocked applies an answer to one request. Caller holds pendingMu.
func resolveLocked(req *PermissionRequest, allow bool, mode string) {
	if allow {
		switch mode {
		case "once":
			DefaultSandbox.AllowOnce(req.Path)
		case "always":
			DefaultSandbox.AllowAlways(req.Path)
		default:
			DefaultSandbox.AllowSession(req.Path)
		}
	}
	req.Allowed = allow
	req.Mode = mode
	req.Resolved = true
	removeLocked(req)
	close(req.done)
}

// ResolvePermissionByID approves or denies the specific queued request with the
// given id. This is the safe API for a dialog: the queue head may change while
// the user is deciding, but the id cannot.
func ResolvePermissionByID(id uint64, allow bool, mode string) bool {
	if id == 0 {
		return false
	}
	pendingMu.Lock()
	defer pendingMu.Unlock()
	for _, req := range pendingQueue {
		if req.ID == id && !req.Resolved {
			resolveLocked(req, allow, mode)
			return true
		}
	}
	return false // already resolved or withdrawn
}

// ResolvePermission approves or denies a queued request by path. An empty path
// resolves the queue head. Used by the sandbox_allow tool and tests; the TUI
// should prefer ResolvePermissionByID.
func ResolvePermission(path string, allow bool, mode string) bool {
	pendingMu.Lock()
	defer pendingMu.Unlock()

	var req *PermissionRequest
	if path == "" {
		req = headLocked()
	} else {
		for _, r := range pendingQueue {
			if r.Path == path {
				req = r
				break
			}
		}
	}
	if req == nil || req.Resolved {
		return false
	}
	resolveLocked(req, allow, mode)
	return true
}

// CancelPendingPermission resolves every queued request as cancelled and
// unblocks all waiters (e.g. on interrupt or shutdown).
func CancelPendingPermission() {
	pendingMu.Lock()
	defer pendingMu.Unlock()
	for _, req := range pendingQueue {
		if !req.Resolved {
			req.Allowed = false
			req.Mode = "cancelled"
			req.Resolved = true
			close(req.done)
		}
	}
	pendingQueue = nil
	pendingPerm = nil
}

// ResolvePath returns the OS-resolved absolute form of path. Callers perform
// their I/O on this value so the sandbox check and the file operation act on the
// same target: a symlink (or a "link/.." sequence) cannot be re-interpreted
// between the check and the open. Returns the input unchanged when no project
// root is configured.
func (sc *SandboxConfig) ResolvePath(path string) string {
	if sc.ProjectRoot == "" {
		return path
	}
	return filepath.Clean(resolveRealPath(absoluteNoClean(path)))
}

// CheckPathAccess enforces the path sandbox and, when access is blocked, asks
// the user for permission.
//
// It returns the path the caller must use for I/O (the OS-resolved form), a
// user-facing denial message when access is refused, and an error when the
// request was cancelled or could not be evaluated. On success denied is empty.
func CheckPathAccess(ctx context.Context, path string) (safePath, denied string, err error) {
	if checkErr := DefaultSandbox.CheckPath(path); checkErr != nil {
		ad, ok := checkErr.(*AccessDenied)
		if !ok {
			return "", "", fmt.Errorf("path check: %w", checkErr)
		}
		allowed, mode := RequestPermission(ctx, ad.Path)
		if !allowed {
			if mode == "cancelled" {
				return "", "", fmt.Errorf("access to %s cancelled", ad.Path)
			}
			return "", ad.DenyHint(), nil
		}
		// Honour the granted mode instead of silently upgrading to a
		// permanent allow.
		switch mode {
		case "once":
			DefaultSandbox.AllowOnce(ad.Path)
		case "always":
			DefaultSandbox.AllowAlways(ad.Path)
		default:
			DefaultSandbox.AllowSession(ad.Path)
		}
	}
	return DefaultSandbox.ResolvePath(path), "", nil
}

// WithPathGate wraps a tool so its path argument ("path" or "file_path") is
// checked against the sandbox before the tool runs.
//
// Tools implemented inside this package already enforce the sandbox
// themselves and must not be wrapped (they would prompt twice); this is for
// tools that live in another package, such as the lsp_* tools.
func WithPathGate(t agent.Tool) agent.Tool {
	inner := t.Execute
	t.Execute = func(ctx context.Context, args map[string]any) (string, error) {
		for _, key := range []string{"path", "file_path"} {
			p, ok := args[key].(string)
			if !ok || p == "" {
				continue
			}
			safePath, denied, err := CheckPathAccess(ctx, p)
			if err != nil {
				return "", err
			}
			if denied != "" {
				return denied, nil
			}
			args[key] = safePath // the inner tool reads the resolved path
			break
		}
		return inner(ctx, args)
	}
	return t
}

// SandboxAllowTool returns an agent.Tool that lets the LLM or TUI
// approve a blocked path.
func SandboxAllowTool() agent.Tool {
	return agent.Tool{
		Name: "sandbox_allow",
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
