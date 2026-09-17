package tool

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/agent"
)

// TestCheckToolPermission delegates to the shared permission evaluation, so a
// change there must reach the pre-execution gate too.
func TestCheckToolPermission(t *testing.T) {
	// A nil config carries no policy: the check must answer instead of
	// panicking (agent.ToolAllowedFor used to dereference it).
	if !CheckToolPermission(nil, "bash") {
		t.Error("a nil config carries no policy and must allow the tool")
	}
	allow := &agent.AgentConfig{Permissions: agent.Ruleset{
		{Action: "*", Resource: "*", Effect: agent.EffectAllow},
	}}
	if !CheckToolPermission(allow, "bash") {
		t.Error("an allow-all ruleset must permit bash")
	}
	deny := &agent.AgentConfig{Permissions: agent.Ruleset{
		{Action: "*", Resource: "*", Effect: agent.EffectAllow},
		{Action: "bash", Resource: "*", Effect: agent.EffectDeny},
	}}
	if CheckToolPermission(deny, "bash") {
		t.Error("a denied tool must be rejected before execution")
	}
	if !CheckToolPermission(deny, "read_file") {
		t.Error("an unrelated tool must stay allowed")
	}
	if CheckToolPermission(&agent.AgentConfig{AllowedTools: []string{"read_file"}}, "bash") {
		t.Error("a whitelist must exclude tools it does not name")
	}
}

// TestAccessDeniedMessages covers what the model and the user are told when the
// sandbox refuses a path.
func TestAccessDeniedMessages(t *testing.T) {
	denied := &AccessDenied{Path: "/etc/passwd", Message: `File "/etc/passwd" is outside the project root.`}
	if denied.Error() != denied.Message {
		t.Errorf("Error() = %q, want the message", denied.Error())
	}
	hint := denied.DenyHint()
	for _, want := range []string{
		"[SECURITY]",
		"allow /etc/passwd",
		"always /etc/passwd",
		"deny /etc/passwd",
	} {
		if !strings.Contains(hint, want) {
			t.Errorf("DenyHint is missing %q:\n%s", want, hint)
		}
	}
}

// TestSandboxAllowOnceAndReset pins the difference between a one-time approval
// and a cached one: only the cached form survives the next check, and
// ResetAllowed drops the whole session allow-list.
func TestSandboxAllowOnceAndReset(t *testing.T) {
	root := t.TempDir()
	sb := &SandboxConfig{ProjectRoot: root, allowedPaths: make(map[string]bool)}
	outside := filepath.Join(root, "..", "tinycode-outside.txt")

	if err := sb.CheckPath(outside); err == nil {
		t.Fatal("a path outside the project root must be denied")
	}
	sb.AllowOnce(outside)
	if err := sb.CheckPath(outside); err == nil {
		t.Error("AllowOnce must not cache the decision; the next check has to ask again")
	}

	sb.AllowSession(outside)
	if err := sb.CheckPath(outside); err != nil {
		t.Errorf("AllowSession should permit the path, got %v", err)
	}
	sb.ResetAllowed()
	if err := sb.CheckPath(outside); err == nil {
		t.Error("ResetAllowed must clear the session allow-list")
	}

	// A path inside the root is allowed without any approval.
	if err := sb.CheckPath(filepath.Join(root, "inside.txt")); err != nil {
		t.Errorf("an in-root path must be allowed, got %v", err)
	}
}

// TestPendingPermissionMetadata covers the accessors the TUI dialog uses to
// describe the request it is showing.
func TestPendingPermissionMetadata(t *testing.T) {
	// Drain anything an earlier test left queued so the empty state below is
	// deterministic.
	ResolvePermission("", false, "deny")
	if PendingPermissionPath() != "" || PendingPermissionAgentLabel() != "" || PendingPermissionID() != 0 {
		t.Fatalf("no request should be pending, got path=%q label=%q id=%d",
			PendingPermissionPath(), PendingPermissionAgentLabel(), PendingPermissionID())
	}

	SetAgentLabel("general")
	t.Cleanup(func() { SetAgentLabel("") })

	const path = "/tmp/tinycode-pending-metadata.txt"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = RequestPermission(ctx, path)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for PendingPermissionID() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if id := PendingPermissionID(); id == 0 {
		t.Fatal("the request never appeared in the queue")
	}
	if got := PendingPermissionPath(); got != path {
		t.Errorf("PendingPermissionPath = %q, want %q", got, path)
	}
	if got := PendingPermissionAgentLabel(); got != "general" {
		t.Errorf("PendingPermissionAgentLabel = %q, want general", got)
	}

	if !ResolvePermissionByID(PendingPermissionID(), false, "deny") {
		t.Fatal("the queued request could not be answered")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RequestPermission did not return after the answer")
	}
	if id := PendingPermissionID(); id != 0 {
		t.Errorf("the queue still reports request %d as pending", id)
	}
}

// TestSandboxAllowTool covers the tool the model calls when the user says
// "allow <path>": the path argument is required, "always" caches the approval
// and the one-time mode does not.
func TestSandboxAllowTool(t *testing.T) {
	DefaultSandbox.ResetAllowed()
	t.Cleanup(DefaultSandbox.ResetAllowed)

	toolDef := SandboxAllowTool()
	if toolDef.Name != "sandbox_allow" {
		t.Fatalf("tool name = %q", toolDef.Name)
	}
	if _, err := toolDef.Execute(context.Background(), map[string]any{}); err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("missing path error = %v", err)
	}

	dir := t.TempDir()
	always := filepath.Join(dir, "always.txt")
	out, err := toolDef.Execute(context.Background(), map[string]any{"path": always, "mode": "always"})
	if err != nil {
		t.Fatalf("always: %v", err)
	}
	if !strings.Contains(out, "always") {
		t.Errorf("result = %q, want it to name the mode", out)
	}
	if !sandboxCachesPath(t, always) {
		t.Error("the session mode must cache the approved path")
	}

	once := filepath.Join(dir, "once.txt")
	if _, err := toolDef.Execute(context.Background(), map[string]any{"path": once, "mode": "once"}); err != nil {
		t.Fatalf("once: %v", err)
	}
	if sandboxCachesPath(t, once) {
		t.Error("a one-time allowance must not be cached")
	}
}

// sandboxCachesPath reports whether the global sandbox remembers an approval for
// path. It inspects the allow-list directly because DefaultSandbox has no
// project root in tests, which makes CheckPath a no-op.
func sandboxCachesPath(t *testing.T, path string) bool {
	t.Helper()
	rawAbs := absoluteNoClean(path)
	realAbs := filepath.Clean(resolveRealPath(rawAbs))
	DefaultSandbox.mu.Lock()
	defer DefaultSandbox.mu.Unlock()
	return DefaultSandbox.allowedPaths[rawAbs] || DefaultSandbox.allowedPaths[realAbs]
}

// TestSmallToolHelpers covers the small helpers used by the search and patch
// tools.
func TestSmallToolHelpers(t *testing.T) {
	// stringsContains must agree with strings.Contains for every case.
	cases := [][2]string{
		{"", ""}, {"abc", ""}, {"", "a"}, {"abc", "bc"}, {"abc", "abcd"},
		{"abc", "abc"}, {"ünïcode", "ïc"}, {"aa", "a"}, {"ab", "ba"},
	}
	for _, c := range cases {
		if got, want := stringsContains(c[0], c[1]), strings.Contains(c[0], c[1]); got != want {
			t.Errorf("stringsContains(%q, %q) = %v, want %v", c[0], c[1], got, want)
		}
	}

	if !hasNullByte([]byte{1, 0, 2}) {
		t.Error("a null byte must be detected")
	}
	if hasNullByte([]byte("plain text")) || hasNullByte(nil) {
		t.Error("text without a null byte must not be reported as binary")
	}

	content := "l1\nl2\nl3\nneedle\nl5\nl6\nl7\nl8\n"
	preview := formatFilePreview(content, "needle")
	if !strings.Contains(preview, "Near line 4") {
		t.Errorf("preview does not point at the match:\n%s", preview)
	}
	if !strings.Contains(preview, "> ") || !strings.Contains(preview, "l5") {
		t.Errorf("preview does not show the surrounding context:\n%s", preview)
	}
	if p := formatFilePreview(content, "not-in-the-file"); p != "" {
		t.Errorf("preview for a missing search = %q, want empty", p)
	}
}
