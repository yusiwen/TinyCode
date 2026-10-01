//go:build unix

package tool

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	result, err := Bash().Execute(context.Background(), map[string]any{
		"command": command,
		"timeout": float64(3),
	})
	if err != nil {
		t.Fatalf("bash tool returned an error instead of output: %v", err)
	}
	// The report names which mechanism killed the command: a reader has to be
	// able to tell whether a double-forked descendant could have survived it
	// (issue #9).
	if !strings.Contains(result, "killed with") {
		t.Errorf("a timed-out command must report the kill mechanism, got:\n%s", result)
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

// TestDoubleForkHelperProcess is not a real test either: re-executed with
// TINYCODE_ESCAPE_HELPER=1 it starts a grandchild that calls setsid(2), waits
// until the grandchild has published its own pid, and then exits. The survivor is
// therefore out of the shell's process group *and* out of its process tree (it
// re-parents to init), which is the escape the process-group kill cannot follow.
func TestDoubleForkHelperProcess(t *testing.T) {
	if os.Getenv("TINYCODE_ESCAPE_HELPER") != "1" {
		t.Skip("helper process for TestBashReapsDoubleForkedDescendants")
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "locate self: %v\n", err)
		os.Exit(1)
	}
	pidFile := os.Getenv("TINYCODE_ESCAPE_PIDFILE")
	if pidFile == "" {
		fmt.Fprintln(os.Stderr, "TINYCODE_ESCAPE_PIDFILE is not set")
		os.Exit(1)
	}

	grandchild := exec.Command(self, "-test.run=TestGrandchildHelperProcess")
	grandchild.Env = append(os.Environ(),
		"TINYCODE_GRANDCHILD_HELPER=1",
		"TINYCODE_GRANDCHILD_PIDFILE="+pidFile)
	if err := grandchild.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "start grandchild: %v\n", err)
		os.Exit(1)
	}

	// Exit as soon as the pid is published: that exit is what re-parents the
	// grandchild to init.
	for i := 0; i < 200; i++ {
		if _, err := os.Stat(pidFile); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	fmt.Println(os.Getpid())
	_ = os.Stdout.Sync()
}

// TestGrandchildHelperProcess is the survivor: it leaves the process group with
// setsid(2), publishes its pid and sleeps until it is killed.
func TestGrandchildHelperProcess(t *testing.T) {
	if os.Getenv("TINYCODE_GRANDCHILD_HELPER") != "1" {
		t.Skip("helper process for TestBashReapsDoubleForkedDescendants")
	}
	if _, err := syscall.Setsid(); err != nil {
		fmt.Fprintf(os.Stderr, "setsid: %v\n", err)
		os.Exit(1)
	}
	pidFile := os.Getenv("TINYCODE_GRANDCHILD_PIDFILE")
	tmp := pidFile + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		os.Exit(1)
	}
	if err := os.Rename(tmp, pidFile); err != nil {
		os.Exit(1)
	}
	time.Sleep(60 * time.Second)
}

// TestBashReapsDoubleForkedDescendants covers the hole the tree walk documents as
// a known limit: the survivor re-parents to init, so neither the tree nor the
// group signal reaches it — only the cgroup the command was started in does. It
// skips where no per-command cgroup can be created, since the tool then degrades
// to the portable kill (and that degradation is what the test above covers).
func TestBashReapsDoubleForkedDescendants(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cgroup v2 reaping is Linux-only")
	}
	probe, err := newBashCgroup()
	if err != nil {
		t.Skipf("no writable cgroup v2 delegation on this host: %v", err)
	}
	_ = probe.remove()

	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")

	// The helper double-forks in the background; the shell keeps running so the
	// tool's timeout is what ends the call.
	command := fmt.Sprintf(
		"TINYCODE_ESCAPE_HELPER=1 TINYCODE_ESCAPE_PIDFILE=%s %s -test.run=TestDoubleForkHelperProcess > /dev/null 2>&1 &\nsleep 60",
		shellQuote(pidFile), shellQuote(bin))

	result, err := Bash().Execute(context.Background(), map[string]any{
		"command": command,
		"timeout": float64(5),
	})
	if err != nil {
		t.Fatalf("bash tool returned an error instead of output: %v", err)
	}

	pid := readDetachPID(t, pidFile)
	if pid == 0 {
		t.Fatal("the double-forked helper never published its pid; the test cannot tell whether it was reaped")
	}
	t.Cleanup(func() {
		if processAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	if !processAlive(pid) {
		t.Skip("the descendant exited before it could be observed; rerun to exercise the kill path")
	}
	if !strings.Contains(result, "cgroup v2") {
		t.Errorf("the report does not name the cgroup mechanism:\n%s", result)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("double-forked descendant %d survived the timeout kill", pid)
}

// TestDoubleForkedDescendantEscapesTheTreeWalk is the negative half of
// TestBashReapsDoubleForkedDescendants, and it runs everywhere: with the cgroup
// constructor forced to fail, the survivor is expected to live on. That is the
// documented limit (a timed-out command can leave a detached process behind) and
// the exact state the Linux cgroup test flips — if a change ever made the tree
// walk catch a double-fork, this test would fail and the cgroup path would have
// to be re-argued rather than kept on faith.
func TestDoubleForkedDescendantEscapesTheTreeWalk(t *testing.T) {
	previous := newBashCgroup
	newBashCgroup = func() (*bashCgroup, error) { return nil, errors.New("test: no cgroup") }
	t.Cleanup(func() { newBashCgroup = previous })

	bin, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	pidFile := filepath.Join(t.TempDir(), "grandchild.pid")
	command := fmt.Sprintf(
		"TINYCODE_ESCAPE_HELPER=1 TINYCODE_ESCAPE_PIDFILE=%s %s -test.run=TestDoubleForkHelperProcess > /dev/null 2>&1 &\nsleep 60",
		shellQuote(pidFile), shellQuote(bin))

	result, err := Bash().Execute(context.Background(), map[string]any{
		"command": command,
		"timeout": float64(5),
	})
	if err != nil {
		t.Fatalf("bash tool returned an error instead of output: %v", err)
	}

	pid := readDetachPID(t, pidFile)
	if pid == 0 {
		t.Fatal("the double-forked helper never published its pid")
	}
	t.Cleanup(func() {
		if processAlive(pid) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})

	if !processAlive(pid) {
		t.Skip("the descendant exited before it could be observed; rerun to exercise the escape")
	}
	if !strings.Contains(result, "process group + tree walk") {
		t.Errorf("the report must name the portable mechanism when no cgroup exists:\n%s", result)
	}

	// Give the kill a moment to prove it cannot reach the survivor, then clean up.
	time.Sleep(500 * time.Millisecond)
	if !processAlive(pid) {
		t.Fatalf("descendant %d did not survive; the tree walk caught a double-fork, so the cgroup path needs re-justifying", pid)
	}
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
