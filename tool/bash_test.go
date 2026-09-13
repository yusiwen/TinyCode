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

// TestBashPlanModeUsesContext verifies the plan-mode restriction travels on the
// context rather than in package state.
func TestBashPlanModeUsesContext(t *testing.T) {
	bash := Bash()

	planCtx := types.WithPlanWriteRestriction(context.Background(), true)
	out, err := bash.Execute(planCtx, map[string]any{"command": "mkdir /tmp/tinycode-planmode-should-not-exist"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "PLAN MODE BLOCKED") {
		t.Errorf("expected PLAN MODE BLOCKED, got %q", out)
	}
	if _, statErr := os.Stat("/tmp/tinycode-planmode-should-not-exist"); statErr == nil {
		os.Remove("/tmp/tinycode-planmode-should-not-exist")
		t.Error("plan mode allowed a mkdir to run")
	}

	// Without the plan marker the same command is not plan-blocked.
	buildCtx := types.WithPlanWriteRestriction(context.Background(), false)
	out, err = bash.Execute(buildCtx, map[string]any{"command": "echo build-ok"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, "PLAN MODE BLOCKED") {
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

func TestCheckPlanModeWriteMkdir(t *testing.T) {
	err := checkPlanModeWrite("mkdir test")
	if err == nil {
		t.Fatal("expected error for mkdir")
	}
}

func TestCheckPlanModeWriteHeredoc(t *testing.T) {
	err := checkPlanModeWrite("cat > file.txt << 'EOF'\nhello\nEOF")
	if err == nil {
		t.Fatal("expected error for heredoc")
	}
}

func TestCheckPlanModeWriteRedirect(t *testing.T) {
	err := checkPlanModeWrite("echo hello > file.txt")
	if err == nil {
		t.Fatal("expected error for redirect")
	}
}

func TestCheckPlanModeWriteAppend(t *testing.T) {
	err := checkPlanModeWrite("echo hello >> file.txt")
	if err == nil {
		t.Fatal("expected error for append redirect")
	}
}

func TestCheckPlanModeReadOnly(t *testing.T) {
	err := checkPlanModeWrite("ls -la")
	if err != nil {
		t.Fatalf("expected no error for ls, got: %v", err)
	}
}

func TestCheckPlanModeReadCat(t *testing.T) {
	err := checkPlanModeWrite("cat file.txt")
	if err != nil {
		t.Fatalf("expected no error for cat, got: %v", err)
	}
}

func TestCheckPlanModeReadGrep(t *testing.T) {
	err := checkPlanModeWrite("grep 'foo' *.go")
	if err != nil {
		t.Fatalf("expected no error for grep, got: %v", err)
	}
}

func TestCheckPlanModeRedirectDevNull(t *testing.T) {
	err := checkPlanModeWrite("grep foo file.txt > /dev/null")
	if err != nil {
		t.Fatalf("expected no error for redirect to /dev/null, got: %v", err)
	}
}

func TestCheckPlanModeRedirectStderr(t *testing.T) {
	err := checkPlanModeWrite("grep foo file.txt 2>/dev/null")
	if err != nil {
		t.Fatalf("expected no error for stderr redirect, got: %v", err)
	}
}

func TestCheckPlanModeRedirectBoth(t *testing.T) {
	err := checkPlanModeWrite("grep foo file.txt &>/dev/null")
	if err != nil {
		t.Fatalf("expected no error for combined redirect to null, got: %v", err)
	}
}

func TestCheckPlanModeWriteRm(t *testing.T) {
	err := checkPlanModeWrite("rm -rf /tmp/test")
	if err == nil {
		t.Fatal("expected error for rm")
	}
}

func TestCheckPlanModeWriteCp(t *testing.T) {
	err := checkPlanModeWrite("cp a.txt b.txt")
	if err == nil {
		t.Fatal("expected error for cp")
	}
}

func TestCheckPlanModeWriteTouch(t *testing.T) {
	err := checkPlanModeWrite("touch newfile.txt")
	if err == nil {
		t.Fatal("expected error for touch")
	}
}

func TestCheckPlanModeWriteMv(t *testing.T) {
	err := checkPlanModeWrite("mv a.txt b.txt")
	if err == nil {
		t.Fatal("expected error for mv")
	}
}

func TestCheckPlanModeWriteChained(t *testing.T) {
	err := checkPlanModeWrite("ls && mkdir test")
	if err == nil {
		t.Fatal("expected error for mkdir in chained command")
	}
}

func TestCheckPlanModeWriteSemicolon(t *testing.T) {
	err := checkPlanModeWrite("ls; rm file.txt")
	if err == nil {
		t.Fatal("expected error for rm in semicolon command")
	}
}

func TestCheckPlanModeWritePipe(t *testing.T) {
	// Pipelines should be allowed
	err := checkPlanModeWrite("cat file.txt | grep foo")
	if err != nil {
		t.Fatalf("expected no error for pipe, got: %v", err)
	}
}
