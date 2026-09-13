package tool

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ── Pattern D Tests ──

func TestAutoAllowCWD(t *testing.T) {
	cwd, _ := os.Getwd()
	sandbox := &SandboxConfig{
		ProjectRoot:    cwd,
		AutoAllowPaths: []string{cwd, filepath.Dir(cwd)},
		allowedPaths:   make(map[string]bool),
	}

	// File within CWD should be auto-allowed
	err := sandbox.CheckPath(filepath.Join(cwd, "test.txt"))
	if err != nil {
		t.Fatalf("expected allow, got: %v", err)
	}
}

func TestAutoAllowParent(t *testing.T) {
	cwd, _ := os.Getwd()
	parent := filepath.Dir(cwd)
	sandbox := &SandboxConfig{
		ProjectRoot:    cwd,
		AutoAllowPaths: []string{cwd, parent},
		allowedPaths:   make(map[string]bool),
	}

	// File within parent should be auto-allowed
	err := sandbox.CheckPath(filepath.Join(parent, "sibling-dir", "test.txt"))
	if err != nil {
		t.Fatalf("expected allow for sibling under parent, got: %v", err)
	}
}

func TestBlockOutsideAutoAllow(t *testing.T) {
	cwd, _ := os.Getwd()
	sandbox := &SandboxConfig{
		ProjectRoot:    cwd,
		AutoAllowPaths: []string{cwd},
		allowedPaths:   make(map[string]bool),
	}

	// /tmp is outside CWD, should be blocked
	err := sandbox.CheckPath(filepath.Join("/tmp", "test.txt"))
	if err == nil {
		t.Fatal("expected AccessDenied for path outside auto-allow")
	}
	_, ok := err.(*AccessDenied)
	if !ok {
		t.Fatalf("expected *AccessDenied, got %T", err)
	}
}

func TestProjectRootAllow(t *testing.T) {
	dir := t.TempDir()
	sandbox := &SandboxConfig{
		ProjectRoot:  dir,
		allowedPaths: make(map[string]bool),
	}

	err := sandbox.CheckPath(filepath.Join(dir, "subdir", "file.txt"))
	if err != nil {
		t.Fatalf("expected allow within project root, got: %v", err)
	}
}

// ── Pattern C Tests ──

func TestPermissionRequestResolve(t *testing.T) {
	// Reset state
	CancelPendingPermission()

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(1)

	// Spawn a goroutine that blocks on RequestPermission
	var allowed bool
	var mode string
	go func() {
		defer wg.Done()
		allowed, mode = RequestPermission(ctx, "/test/path.txt")
	}()

	// Give it time to block
	time.Sleep(50 * time.Millisecond)

	// Simulate the user's response via TUI
	if !HasPendingPermission() {
		t.Fatal("expected pending permission after RequestPermission")
	}
	if path := PendingPermissionPath(); path != "/test/path.txt" {
		t.Fatalf("expected path /test/path.txt, got %s", path)
	}

	resolved := ResolvePermission("/test/path.txt", true, "once")
	if !resolved {
		t.Fatal("expected ResolvePermission to succeed")
	}

	// Wait for the goroutine to unblock
	wg.Wait()

	if !allowed {
		t.Fatal("expected allowed=true after ResolvePermission")
	}
	if mode != "once" {
		t.Fatalf("expected mode=once, got %s", mode)
	}
}

func TestPermissionRequestCancel(t *testing.T) {
	CancelPendingPermission()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)

	var allowed bool
	var mode string
	go func() {
		defer wg.Done()
		allowed, mode = RequestPermission(ctx, "/test/path.txt")
	}()

	time.Sleep(50 * time.Millisecond)

	// Cancel the context (simulates Ctrl+C / interrupt)
	cancel()
	wg.Wait()

	if allowed {
		t.Fatal("expected allowed=false after cancel")
	}
	if mode != "cancelled" {
		t.Fatalf("expected mode=cancelled, got %s", mode)
	}
}

func TestPermissionRequestDeny(t *testing.T) {
	CancelPendingPermission()

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(1)

	var allowed bool
	go func() {
		defer wg.Done()
		allowed, _ = RequestPermission(ctx, "/test/path.txt")
	}()

	time.Sleep(50 * time.Millisecond)

	// User denies
	ResolvePermission("", false, "denied")
	wg.Wait()

	if allowed {
		t.Fatal("expected allowed=false after deny")
	}
}

// ── WriteFile Integration Test ──

func TestWriteFilePermissionFlow(t *testing.T) {
	// Temporarily set sandbox with restricted ProjectRoot
	tmpDir := t.TempDir()
	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{
		ProjectRoot:    tmpDir,
		AutoAllowPaths: []string{tmpDir},
		allowedPaths:   make(map[string]bool),
	}
	defer func() { DefaultSandbox = saved }()

	tool := WriteFile()

	// Write to a path within auto-allow (should work immediately)
	allowedPath := filepath.Join(tmpDir, "allowed.txt")
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":    allowedPath,
		"content": "hello",
	})
	if err != nil {
		t.Fatalf("WriteFile within auto-allow failed: %v", err)
	}

	// Verify file was actually written
	data, _ := os.ReadFile(allowedPath)
	if string(data) != "hello" {
		t.Errorf("expected 'hello', got %q", data)
	}
}

func TestWriteFileBlockedWithoutPermission(t *testing.T) {
	tmpDir := t.TempDir()
	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{
		ProjectRoot:    tmpDir,
		AutoAllowPaths: nil,
		allowedPaths:   make(map[string]bool),
	}
	defer func() { DefaultSandbox = saved }()

	tool := WriteFile()

	// Try to write outside project root
	blockedPath := filepath.Join(os.TempDir(), "tinycode-test-blocked.txt")
	var wg sync.WaitGroup
	wg.Add(1)

	errCh := make(chan error, 1)
	go func() {
		defer wg.Done()
		_, err := tool.Execute(context.Background(), map[string]any{
			"path":    blockedPath,
			"content": "should be blocked",
		})
		errCh <- err
	}()

	time.Sleep(100 * time.Millisecond)

	if !HasPendingPermission() {
		t.Fatal("expected pending permission after blocked write")
	}

	// User allows
	ResolvePermission("", true, "always")
	wg.Wait()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("expected write to succeed after permission, got: %v", err)
		}
	default:
	}
}

// ── Symlink, "once" semantics and apply_patch gate ──

// TestCheckPathRejectsSymlinkEscape verifies that a symlink inside the project
// root pointing outside it is rejected.
func TestCheckPathRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	sandbox := &SandboxConfig{ProjectRoot: root, allowedPaths: make(map[string]bool)}

	if err := sandbox.CheckPath(link); err == nil {
		t.Error("expected AccessDenied for a symlink escaping the project root")
	}
	// A symlink that stays inside the root must still be allowed.
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("ok"), 0644); err != nil {
		t.Fatal(err)
	}
	insideLink := filepath.Join(root, "inside-link.txt")
	if err := os.Symlink(inside, insideLink); err != nil {
		t.Fatal(err)
	}
	if err := sandbox.CheckPath(insideLink); err != nil {
		t.Errorf("expected in-root symlink to be allowed, got %v", err)
	}
}

// TestCheckPathRejectsSymlinkDotDotEscape covers the "link/.." escape: the OS
// applies ".." to the already-resolved link target, so a lexical check that
// collapses the path first would wrongly allow it.
func TestCheckPathRejectsSymlinkDotDotEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "up")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	sandbox := &SandboxConfig{ProjectRoot: root, allowedPaths: make(map[string]bool)}

	// Raw (uncleaned) path: the kernel reads outside/secret.txt.
	escape := root + "/up/../outside/secret.txt"
	if err := sandbox.CheckPath(escape); err == nil {
		t.Errorf("expected AccessDenied for %q (link/.. escape)", escape)
	}

	// A not-yet-existing file reached the same way must be blocked too.
	escapeNew := root + "/up/../outside/brand-new.txt"
	if err := sandbox.CheckPath(escapeNew); err == nil {
		t.Errorf("expected AccessDenied for %q (link/.. escape, new file)", escapeNew)
	}

	// Writing directly through the escaping link must be blocked.
	if err := sandbox.CheckPath(filepath.Join(root, "up", "secret.txt")); err == nil {
		t.Error("expected AccessDenied for a path through an escaping symlink")
	}
}

// TestCheckPathAllowsInRootDotDot is the positive control: ".." that stays
// inside the root is still allowed.
func TestCheckPathAllowsInRootDotDot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "b"), 0755); err != nil {
		t.Fatal(err)
	}
	sandbox := &SandboxConfig{ProjectRoot: root, allowedPaths: make(map[string]bool)}

	for _, p := range []string{
		root + "/a/../b/file.txt",
		root + "/a/./../b/file.txt",
		filepath.Join(root, "b", "file.txt"),
	} {
		if err := sandbox.CheckPath(p); err != nil {
			t.Errorf("expected %q to be allowed, got %v", p, err)
		}
	}

	if err := sandbox.CheckPath(root + "/../outside.txt"); err == nil {
		t.Error("expected AccessDenied for a path escaping via ..")
	}
}

// TestCheckPathAccessOnceIsNotCached verifies that a one-time approval does not
// become a permanent allow.
func TestCheckPathAccessOnceIsNotCached(t *testing.T) {
	tmpDir := t.TempDir()
	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{ProjectRoot: tmpDir, allowedPaths: make(map[string]bool)}
	defer func() { DefaultSandbox = saved }()
	defer CancelPendingPermission()

	target := filepath.Join(os.TempDir(), "tinycode-once-test.txt")

	go func() {
		time.Sleep(100 * time.Millisecond)
		ResolvePermission("", true, "once")
	}()

	safePath, denied, err := CheckPathAccess(context.Background(), target)
	if err != nil {
		t.Fatalf("CheckPathAccess returned error: %v", err)
	}
	if denied != "" {
		t.Fatalf("expected access to be granted once, got denial %q", denied)
	}
	if safePath == "" {
		t.Error("expected a resolved path for I/O")
	}

	DefaultSandbox.mu.Lock()
	cached := DefaultSandbox.allowedPaths[target]
	DefaultSandbox.mu.Unlock()
	if cached {
		t.Error("one-time approval was cached; the next access will not re-prompt")
	}
}

// TestApplyPatchRespectsSandbox verifies that apply_patch is gated by the path
// sandbox (it used to write anywhere without any check).
func TestApplyPatchRespectsSandbox(t *testing.T) {
	tmpDir := t.TempDir()
	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{ProjectRoot: tmpDir, allowedPaths: make(map[string]bool)}
	defer func() { DefaultSandbox = saved }()

	outside := filepath.Join(os.TempDir(), "tinycode-patch-escape.txt")
	os.Remove(outside)
	defer os.Remove(outside)

	patch := ApplyPatch()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// No user is available to approve, so the tool must refuse (and must not
	// create the file).
	if _, err := patch.Execute(ctx, map[string]any{
		"patch_text": "*** Begin Patch\n*** Add File: " + outside + "\n+payload\n*** End Patch\n",
	}); err == nil {
		t.Error("expected apply_patch outside the project root to fail")
	}
	if _, statErr := os.Stat(outside); statErr == nil {
		t.Error("apply_patch created a file outside the project root")
	}
}

// TestApplyPatchAllowedInsideRoot is the positive control for the gate above.
func TestApplyPatchAllowedInsideRoot(t *testing.T) {
	tmpDir := t.TempDir()
	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{ProjectRoot: tmpDir, allowedPaths: make(map[string]bool)}
	defer func() { DefaultSandbox = saved }()

	target := filepath.Join(tmpDir, "inside.txt")
	patch := ApplyPatch()
	res, err := patch.Execute(context.Background(), map[string]any{
		"patch_text": "*** Begin Patch\n*** Add File: " + target + "\n+payload\n*** End Patch\n",
	})
	if err != nil {
		t.Fatalf("apply_patch inside root failed: %v (result %q)", err, res)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read patched file: %v", err)
	}
	if string(data) != "payload\n" {
		t.Errorf("file content = %q, want %q", data, "payload\n")
	}
}

// TestResolveRealPathKernelOrder pins the semantics of the security-critical
// resolver: ".." must be applied after the symlink on its left is resolved,
// and components that do not exist yet must be preserved.
func TestResolveRealPathKernelOrder(t *testing.T) {
	base := t.TempDir()
	// Normalize the temp dir itself so expectations are not confused by
	// platform symlinks such as macOS /var -> /private/var.
	if real, err := filepath.EvalSymlinks(base); err == nil {
		base = real
	}
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "up")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	cases := []struct {
		in   string
		want string
	}{
		// "up" resolves to outside, then ".." goes to base, then outside/secret.
		{root + "/up/../outside/secret.txt", filepath.Join(outside, "secret.txt")},
		// Trailing components that do not exist are kept verbatim.
		{root + "/up/../outside/new.txt", filepath.Join(outside, "new.txt")},
		// No symlink involved: normal resolution.
		{filepath.Join(root, "sub", "file.txt"), filepath.Join(root, "sub", "file.txt")},
		// Root stays root.
		{string(filepath.Separator), string(filepath.Separator)},
	}
	for _, tc := range cases {
		got := filepath.Clean(resolveRealPath(tc.in))
		want := filepath.Clean(tc.want)
		if got != want {
			t.Errorf("resolveRealPath(%q) = %q, want %q", tc.in, got, want)
		}
	}

	// A symlink loop must terminate (EvalSymlinks fails and we fall back).
	loop := filepath.Join(base, "loop")
	if err := os.Symlink(loop, loop); err == nil {
		_ = resolveRealPath(filepath.Join(loop, "x"))
	}
}

// TestResolvePermissionByID covers the F1 fix: an answer must apply to the
// request the user was shown, even when it is not the queue head, and a stale
// id (already resolved/withdrawn) must be rejected.
func TestResolvePermissionByID(t *testing.T) {
	CancelPendingPermission()
	defer CancelPendingPermission()

	ctx := context.Background()
	type answer struct {
		allowed bool
		mode    string
	}
	firstCh := make(chan answer, 1)
	secondCh := make(chan answer, 1)

	go func() {
		a, m := RequestPermission(ctx, "/first/path")
		firstCh <- answer{a, m}
	}()
	waitForHeadPath(t, "/first/path")
	headID := PendingPermissionID()
	if headID == 0 {
		t.Fatal("expected a pending request id")
	}

	go func() {
		a, m := RequestPermission(ctx, "/second/path")
		secondCh <- answer{a, m}
	}()
	time.Sleep(100 * time.Millisecond) // let the second request enqueue

	// Answer the *tail* by path: the head must stay pending.
	if !ResolvePermission("/second/path", true, "session") {
		t.Fatal("expected to resolve the queued /second/path request")
	}
	got := <-secondCh
	if !got.allowed || got.mode != "session" {
		t.Fatalf("second request got (%v, %q), want (true, session)", got.allowed, got.mode)
	}
	if !HasPendingPermission() {
		t.Fatal("the head request must still be pending")
	}
	if p := PendingPermissionPath(); p != "/first/path" {
		t.Fatalf("head = %q, want /first/path", p)
	}

	// Answer the head by id.
	if !ResolvePermissionByID(headID, false, "denied") {
		t.Fatal("expected to resolve the head by id")
	}
	got = <-firstCh
	if got.allowed || got.mode != "denied" {
		t.Fatalf("first request got (%v, %q), want (false, denied)", got.allowed, got.mode)
	}

	// A stale id must be refused, and the queue must be empty.
	if ResolvePermissionByID(headID, true, "always") {
		t.Error("resolving an already-answered request id must fail")
	}
	if PendingPermissionID() != 0 || HasPendingPermission() {
		t.Error("queue should be empty")
	}
}

// waitForHeadPath blocks until the queue head is the expected path.
func waitForHeadPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if PendingPermissionPath() == path {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for pending path %q (head=%q)", path, PendingPermissionPath())
}

// TestCheckPathAccessReturnsResolvedPath pins the contract that callers do their
// I/O on the OS-resolved path, so a symlink cannot be re-interpreted between the
// check and the open.
func TestCheckPathAccessReturnsResolvedPath(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "real.txt")
	if err := os.WriteFile(real, []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{ProjectRoot: root, allowedPaths: make(map[string]bool)}
	defer func() { DefaultSandbox = saved }()

	safePath, denied, err := CheckPathAccess(context.Background(), link)
	if err != nil || denied != "" {
		t.Fatalf("expected access, got denied=%q err=%v", denied, err)
	}
	want, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if safePath != filepath.Clean(want) {
		t.Errorf("resolved path = %q, want %q", safePath, filepath.Clean(want))
	}

	// A path outside the root must be refused (and return no usable path).
	outside := filepath.Join(base, "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if safe, _, _ := CheckPathAccess(ctx, outside); safe != "" {
		t.Errorf("refused path returned a usable path %q", safe)
	}
}
