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
