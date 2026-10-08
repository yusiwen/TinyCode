// The process-level half of issue #141's acceptance.
//
// The daemon package's own tests assert the rule — an idle daemon closes the
// session it still holds and exits. These two drive the real binary instead,
// because the failure that mattered was a *process* left in the machine's process
// table by a *client that had already exited*: an orphaned daemon at ppid 1, with
// the program under test still running and writing four days later. Nothing
// in-process can show that.
//
// Both are deliberately cheap: the daemon they watch is given a two-second TTL, so
// the test waits for a deadline instead of for ten minutes.
package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// toolPath is the binary under test, built once here.
//
// TUIPROBE_BIN overrides it with an existing binary: that is how a pre-fix build
// is run against these guards, which is the mutation evidence recorded in the
// pull request for issue #141.
var toolPath string

func TestMain(m *testing.M) { os.Exit(runTests(m)) }

func runTests(m *testing.M) int {
	if bin := os.Getenv("TUIPROBE_BIN"); bin != "" {
		toolPath = bin
		return m.Run()
	}
	dir, err := os.MkdirTemp("", "tuiprobe-e2e-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "temp dir: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	toolPath = filepath.Join(dir, "tuiprobe")
	build := exec.Command("go", "build", "-o", toolPath, ".")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "building the tool: %v\n", err)
		return 1
	}
	return m.Run()
}

// TestScenarioRunLeavesNoDaemonBehind is the acceptance line of issue #141: after
// a run ends, neither the daemon it started nor a program of its own is left.
//
// The scenario lets the program exit by itself, as the shipped examples do, so the
// client ends normally — a run that is killed is the next test's business.
func TestScenarioRunLeavesNoDaemonBehind(t *testing.T) {
	socket := shortSocket(t)
	scenario := filepath.Join(t.TempDir(), "quick.scenario")
	body := "open --size 80x24 -- /bin/sh -c 'echo ready; sleep 0.3; exit 3'\n" +
		"wait --text ready --timeout 5s\n" +
		"wait-exit 5s\n" +
		"expect-exit 3\n"
	if err := os.WriteFile(scenario, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := runTool(t, nil, "run", "--socket", socket, scenario)
	if code != 0 {
		t.Fatalf("run: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "ok: 4 steps") {
		t.Fatalf("run printed %q, want the step count", stdout)
	}

	waitForNoDaemon(t, socket, 5*time.Second)
}

// TestAbandonedDaemonClosesItsSession covers the leak itself: a daemon whose client
// is gone — killed, crashed, or walked away from — must not keep the program under
// test alive past its TTL.
//
// The daemon here is started detached (setsid), like the one the client launches,
// so what is measured is an orphan at ppid 1 holding a live child: launching it as
// an ordinary child would not do, because a process we never wait for stays a
// zombie, and `kill -0` on a zombie succeeds.
func TestAbandonedDaemonClosesItsSession(t *testing.T) {
	socket := shortSocket(t)

	launch := fmt.Sprintf("%s daemon --socket %s --ttl 2s >/dev/null 2>&1 & echo $!",
		shellQuote(toolPath), shellQuote(socket))
	shell := exec.Command("/bin/sh", "-c", launch)
	shell.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	launched, err := shell.Output()
	if err != nil {
		t.Fatalf("launching a detached daemon: %v", err)
	}
	daemonPid, err := strconv.Atoi(strings.TrimSpace(string(launched)))
	if err != nil || daemonPid == 0 {
		t.Fatalf("the launcher printed pid %q, which is not a pid", launched)
	}
	t.Cleanup(func() { _ = syscall.Kill(daemonPid, syscall.SIGKILL) })

	waitForSocket(t, socket)
	stdout, stderr, code := runTool(t, nil, "open", "--socket", socket,
		"--name", "leak", "--size", "80x24", "--", "/bin/sh", "-c", "while :; do sleep 0.2; done")
	if code != 0 {
		t.Fatalf("open: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	childPid := pidFromOpen(t, stdout)
	t.Cleanup(func() { _ = syscall.Kill(childPid, syscall.SIGKILL) })

	// The control: both are alive now, so "gone" later means the deadline fired
	// rather than the daemon never having started.
	if !alive(daemonPid) || !alive(childPid) {
		t.Fatalf("after open: daemon %d alive=%v, program %d alive=%v; want both running",
			daemonPid, alive(daemonPid), childPid, alive(childPid))
	}

	deadline := time.Now().Add(20 * time.Second)
	for alive(daemonPid) || alive(childPid) {
		if time.Now().After(deadline) {
			t.Fatalf("20s after its 2s TTL, the abandoned daemon (pid %d, ppid %s, alive=%v) still holds the program under test (pid %d, alive=%v)",
				daemonPid, ppidOf(daemonPid), alive(daemonPid), childPid, alive(childPid))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// shellQuote wraps a path for /bin/sh, so a tool built under a directory with a
// space in its name still launches.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// ppidOf reports a process's parent, for a failure message that shows an orphan.
func ppidOf(pid int) string {
	out, err := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "?"
	}
	return strings.TrimSpace(string(out))
}

// shortSocket returns a socket path short enough for the unix-socket limit on both
// macOS and Linux, which a t.TempDir() path is not.
func shortSocket(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("tpe2e-%d-%d.sock", os.Getpid(), time.Now().UnixNano()%1000000))
	t.Cleanup(func() { _ = os.Remove(socket) })
	return socket
}

// runTool runs the tool and returns what it printed and its exit code.
func runTool(t *testing.T, env []string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(toolPath, args...)
	cmd.Env = append(os.Environ(), env...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("%s: %v", strings.Join(args, " "), err)
		}
		code = exitErr.ExitCode()
	}
	return stdout.String(), stderr.String(), code
}

// pidFromOpen reads the program's pid out of `open`'s "leak pid=1234 80x24".
func pidFromOpen(t *testing.T, stdout string) int {
	t.Helper()
	m := regexp.MustCompile(`pid=(\d+)`).FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("open printed %q, want it to name the program's pid", stdout)
	}
	pid, err := strconv.Atoi(m[1])
	if err != nil || pid == 0 {
		t.Fatalf("open printed pid %q, which is not a pid", m[1])
	}
	return pid
}

// alive reports whether a process exists, without disturbing it.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// waitForSocket blocks until the daemon is accepting.
func waitForSocket(t *testing.T, socket string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if conn, err := net.DialTimeout("unix", socket, 100*time.Millisecond); err == nil {
			conn.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no daemon ever listened on %s", socket)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForNoDaemon fails with the leftover processes it found, so a regression
// reports a pid instead of a bare timeout.
func waitForNoDaemon(t *testing.T, socket string, budget time.Duration) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for {
		leftovers := daemonsOn(socket)
		conn, err := net.DialTimeout("unix", socket, 100*time.Millisecond)
		if err == nil {
			conn.Close()
		}
		if err != nil && len(leftovers) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s after the run ended, its daemon is still there: listening=%v, processes=%v",
				budget, err == nil, leftovers)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// daemonsOn lists the processes whose command line names this socket, which is how
// a leftover daemon is described in a failure without touching another session's.
func daemonsOn(socket string) []string {
	out, err := exec.Command("ps", "-wwax", "-o", "pid=,command=").Output()
	if err != nil {
		return []string{"ps failed: " + err.Error()}
	}
	var found []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, socket) && strings.Contains(line, "daemon") {
			found = append(found, strings.TrimSpace(line))
		}
	}
	return found
}
