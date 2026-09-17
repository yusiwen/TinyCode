//go:build unix

package tool

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestDetachHelperProcess is not a real test. Re-executed as a child with
// TINYCODE_DETACH_HELPER=1 it calls setsid(2), which moves it out of the process
// group the bash tool kills, then writes its pid and sleeps — standing in for a
// command that escapes the shell's process group.
func TestDetachHelperProcess(t *testing.T) {
	if os.Getenv("TINYCODE_DETACH_HELPER") != "1" {
		t.Skip("helper process for TestBashReapsDetachedDescendants")
	}
	if _, err := syscall.Setsid(); err != nil {
		fmt.Fprintf(os.Stderr, "setsid: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(os.Getpid())
	_ = os.Stdout.Sync()
	time.Sleep(60 * time.Second)
}

// TestBashReapsDetachedDescendants covers the escape documented as a known
// limit: a command that calls setsid(2) leaves the shell's process group, so
// killing the group alone left it running after the tool call returned.
func TestBashReapsDetachedDescendants(t *testing.T) {
	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), "helper.pid")

	// The helper detaches and writes its pid; the shell keeps running so the
	// tool's timeout is what ends the call.
	command := fmt.Sprintf("TINYCODE_DETACH_HELPER=1 %s -test.run=TestDetachHelperProcess > %s 2>&1 &\nsleep 60",
		shellQuote(bin), shellQuote(pidFile))

	if _, err := Bash().Execute(context.Background(), map[string]any{
		"command": command,
		"timeout": float64(3),
	}); err != nil {
		t.Fatalf("bash tool returned an error instead of output: %v", err)
	}

	pid := readDetachPID(t, pidFile)
	if pid == 0 {
		t.Fatal("the detached helper never started; the test cannot tell whether it was reaped")
	}
	// Make sure a failure does not leave a sleeping process behind.
	t.Cleanup(func() {
		if processAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	if !processAlive(pid) {
		t.Skip("the helper exited before it could be observed; rerun to exercise the kill path")
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("detached descendant %d survived the timeout kill", pid)
}

// TestDescendantPIDsFindsChildren checks the process-tree walk directly, so a
// failure of the reaping test above can be attributed to the walk or the kill.
func TestDescendantPIDsFindsChildren(t *testing.T) {
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skipf("ps is not available: %v", err)
	}

	child := exec.Command("sleep", "60")
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		_ = child.Process.Kill()
		_ = child.Wait()
	})

	found := false
	for _, pid := range descendantPIDs(os.Getpid()) {
		if pid == child.Process.Pid {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("descendantPIDs(%d) = %v, missing child %d",
			os.Getpid(), descendantPIDs(os.Getpid()), child.Process.Pid)
	}

	// Nonsensical roots are refused instead of walking the whole table.
	for _, root := range []int{0, 1, -1} {
		if got := descendantPIDs(root); got != nil {
			t.Errorf("descendantPIDs(%d) = %v, want nil", root, got)
		}
	}
}

// shellQuote wraps s in single quotes so a path with spaces reaches the shell
// as one argument.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// readDetachPID reads the pid the helper wrote, retrying while the file is still
// being created.
func readDetachPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			if pid, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil {
				return pid
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

// processAlive reports whether pid still exists. Signal 0 performs the
// permission and existence checks without delivering a signal.
func processAlive(pid int) bool {
	if pid <= 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
