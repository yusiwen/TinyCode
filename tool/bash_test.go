package tool

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// TestBashPlanModeUsesContext verifies the mode travels on the run's policy
// rather than in package state.
func TestBashPlanModeUsesContext(t *testing.T) {
	// The test host has no kernel boundary and the switch is off, so a read-only
	// run refuses to execute at all. That is the point: without a boundary there
	// is nothing for a read-only mode to stand on.
	previous := ConfineCommands()
	SetConfineCommands(false)
	t.Cleanup(func() { SetConfineCommands(previous) })

	bash := Bash()

	planCtx := types.WithSandboxPolicy(context.Background(), types.SandboxPolicy{Mode: types.SandboxReadOnly})
	out, err := bash.Execute(planCtx, map[string]any{"command": "mkdir /tmp/tinycode-planmode-should-not-exist"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The refusal names the mode it happened under, the recovery that works —
	// switching mode — and, because the test host cannot confine a command, the
	// boundary that is missing. The last one is what separates "plan mode will not
	// run this" from "plan mode cannot keep its promise here".
	for _, want := range []string{types.RefusalMarker, "mode: " + string(types.SandboxReadOnly), "ask: " + types.AskMode, "/build", "boundary it can enforce"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan-mode refusal is missing %q, got %q", want, out)
		}
	}
	if _, statErr := os.Stat("/tmp/tinycode-planmode-should-not-exist"); statErr == nil {
		os.Remove("/tmp/tinycode-planmode-should-not-exist")
		t.Error("plan mode allowed a mkdir to run")
	}

	// Under a workspace-write policy the same command is not plan-blocked.
	buildCtx := types.WithSandboxPolicy(context.Background(), types.SandboxPolicy{Mode: types.SandboxWorkspaceWrite})
	out, err = bash.Execute(buildCtx, map[string]any{"command": "echo build-ok"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, types.RefusalMarker) {
		t.Errorf("build mode was blocked: %q", out)
	}
}

// TestLimitedBufferTruncates checks the output cap used by bash.
func TestLimitedBufferTruncates(t *testing.T) {
	var b limitedBuffer
	b.limit = 8

	n, err := b.Write([]byte("1234567890"))
	if err != nil || n != 10 {
		t.Fatalf("Write = (%d, %v), want (10, nil)", n, err)
	}
	if got := b.String(); got != "12345678" {
		t.Errorf("buffer = %q, want %q", got, "12345678")
	}
	if !b.truncated {
		t.Error("expected truncated=true")
	}

	// A second write past the limit must not change the buffer.
	if _, err := b.Write([]byte("more")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := b.String(); got != "12345678" {
		t.Errorf("buffer = %q, want it to stay %q", got, "12345678")
	}
}

// fileSize returns the size of path, or -1 when it does not exist.
func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return -1
	}
	return info.Size()
}

// TestBashTimeoutKillsProcessGroup ensures a timed-out command does not leave
// background children running.
func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are POSIX-only")
	}

	dir := t.TempDir()
	marker := filepath.Join(dir, "alive.txt")
	// The foreground sleep keeps the call alive until the timeout; the
	// backgrounded loop keeps writing so a surviving orphan is observable.
	cmdStr := "bash -c 'echo start >> " + marker + "; for i in 1 2 3 4 5 6 7 8; do sleep 0.4; echo tick >> " + marker + "; done' & sleep 30"

	bash := Bash()
	if _, err := bash.Execute(context.Background(), map[string]any{
		"command": cmdStr,
		"timeout": float64(1),
	}); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	time.Sleep(1200 * time.Millisecond)
	before := fileSize(marker)
	time.Sleep(1200 * time.Millisecond)
	after := fileSize(marker)

	if before > 0 && after > before {
		t.Errorf("orphan child kept running after timeout: marker grew %d -> %d bytes", before, after)
	}
}
