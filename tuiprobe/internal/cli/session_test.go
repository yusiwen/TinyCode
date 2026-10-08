package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/TinyCode/tuiprobe/internal/daemon"
)

// startDaemon runs a daemon on a short socket path and returns the socket.
func startDaemon(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("tpc-%d-%d.sock", os.Getpid(), time.Now().UnixNano()%100000))
	srv := daemon.New(socket, time.Minute)
	ln, err := srv.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Stop(); _ = ln.Close() })
	return socket
}

// TestSessionCommandsEndToEnd drives a program through separate command
// invocations, which is how an agent uses the tool: each call is its own process,
// and the session outlives them all.
func TestSessionCommandsEndToEnd(t *testing.T) {
	socket := startDaemon(t)
	base := []string{"--socket", socket, "--name", "app"}

	code, out, errOut := run(append([]string{"open"}, append(base, "--size", "90x25", "--",
		"/bin/sh", "-c", "echo ready; read l; echo got:$l; exit 4")...), "")
	if code != 0 {
		t.Fatalf("open: exit %d, stderr %s", code, errOut)
	}
	if !strings.Contains(out, "app pid=") || !strings.Contains(out, "90x25") {
		t.Errorf("open printed %q, want the name, pid and geometry", out)
	}

	if code, _, errOut := run(append([]string{"wait"}, append(base, "--text", "ready", "--timeout", "5s")...), ""); code != 0 {
		t.Fatalf("wait: exit %d, stderr %s", code, errOut)
	}

	code, out, _ = run(append([]string{"text"}, base...), "")
	if code != 0 || !strings.Contains(out, "ready") {
		t.Errorf("text: exit %d, output %q", code, out)
	}
	// The human-readable form does not trail a screenful of blank rows; --json
	// keeps the whole geometry for a machine.
	if !strings.HasSuffix(out, "ready\n") {
		t.Errorf("text output = %q, want it trimmed after the content", out)
	}

	// --json gives a caller the same information in a parseable form.
	code, out, _ = run(append([]string{"text"}, append(base, "--json")...), "")
	if code != 0 || !strings.Contains(out, `"text"`) || !strings.Contains(out, `"cols": 90`) {
		t.Errorf("text --json: exit %d, output %q", code, out)
	}

	if code, _, errOut := run(append([]string{"send"}, append(base, "--text", "hello", "--key", "enter")...), ""); code != 0 {
		t.Fatalf("send: exit %d, stderr %s", code, errOut)
	}
	if code, _, errOut := run(append([]string{"wait"}, append(base, "--text", "got:hello", "--timeout", "5s")...), ""); code != 0 {
		t.Fatalf("wait for the answer: exit %d, stderr %s", code, errOut)
	}

	// trace shows the raw stream, text does not.
	code, out, _ = run(append([]string{"trace"}, append(base, "--n", "32")...), "")
	if code != 0 || out == "" {
		t.Errorf("trace: exit %d, output %q", code, out)
	}

	code, out, _ = run([]string{"sessions", "--socket", socket}, "")
	if code != 0 || !strings.Contains(out, "app pid=") {
		t.Errorf("sessions: exit %d, output %q", code, out)
	}

	// Wait until the shell has really left before asking `close` for its code. A
	// close that arrives first kills the program and reports -1, which is the same
	// race the scenario suite lost on CI (issue #135); `text --json` answers
	// `hasExited` as soon as the session's reaper has the status.
	waitForExit(t, base)
	code, out, _ = run(append([]string{"close"}, append(base, "--expect-exit", "4")...), "")
	if code != 0 || !strings.Contains(out, "exited with 4") {
		t.Errorf("close: exit %d, output %q", code, out)
	}

	if code, out, _ := run([]string{"sessions", "--socket", socket, "--json"}, ""); code != 0 || !strings.Contains(out, `"sessionCount": 0`) {
		t.Errorf("sessions after close: exit %d, output %q", code, out)
	}
}

func TestSessionCommandsReportFailures(t *testing.T) {
	socket := startDaemon(t)

	// A missing session is its own exit code, so a caller can tell it from a
	// usage error without reading the message.
	if code, _, errOut := run([]string{"text", "--socket", socket, "--name", "nope"}, ""); code != daemon.CodeNoSuch {
		t.Errorf("text on a missing session: exit %d (stderr %q), want %d", code, errOut, daemon.CodeNoSuch)
	}
	// A timeout is CodeTimeout, not a generic failure.
	if code, _, errOut := run([]string{"open", "--socket", socket, "--name", "idle", "--", "/bin/sh", "-c", "sleep 30"}, ""); code != 0 {
		t.Fatalf("open idle: exit %d, stderr %s", code, errOut)
	}
	if code, _, _ := run([]string{"wait", "--socket", socket, "--name", "idle", "--text", "never", "--timeout", "150ms"}, ""); code != daemon.CodeTimeout {
		t.Errorf("wait timeout: exit %d, want %d", code, daemon.CodeTimeout)
	}
	// A wrong exit code is an assertion failure.
	if code, _, _ := run([]string{"close", "--socket", socket, "--name", "idle", "--expect-exit", "0"}, ""); code != daemon.CodeFailure {
		t.Errorf("close with the wrong expectation: exit %d, want %d", code, daemon.CodeFailure)
	}
	// Usage errors.
	for _, args := range [][]string{
		{"open", "--socket", socket},
		{"send", "--socket", socket, "--name", "x"},
		{"wait", "--socket", socket, "--name", "x"},
		{"open", "--socket", socket, "--name", "y", "--size", "80", "--", "/bin/sh"},
		{"send", "--socket", socket, "--name", "x", "--key", "enter", "--repeat", "0"},
	} {
		if code, _, errOut := run(args, ""); code != daemon.CodeFailure || errOut == "" {
			t.Errorf("%v: exit %d, stderr %q, want a usage failure", args, code, errOut)
		}
	}
}

func TestDaemonCommandServesUntilStopped(t *testing.T) {
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("tpd-cli-%d.sock", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(socket) })

	done := make(chan int, 1)
	go func() {
		code, _, _ := run([]string{"daemon", "--socket", socket, "--ttl", "1s"}, "")
		done <- code
	}()

	client := &daemon.Client{Socket: socket, StartDaemon: func(string) error { return nil }}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if resp, err := client.Call(daemon.Request{Cmd: "ping"}); err == nil && resp.OK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon command never started listening")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if _, err := client.Call(daemon.Request{Cmd: "stop"}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("daemon exited with %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon command did not return after stop")
	}
}

// waitForExit blocks until the session reports that its program has left, so an
// exit-code assertion does not race the program's own exit: `close` on a running
// program kills it and reports -1 (issue #135).
func waitForExit(t *testing.T, base []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code, out, _ := run(append([]string{"text"}, append(base, "--json")...), "")
		if code == 0 && strings.Contains(out, `"hasExited": true`) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the program never reported an exit (text --json: exit %d, output %q)", code, out)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestMarkAndWaitSinceSeeOnlyNewText drives the pair through the CLI, which is how an
// agent uses it: mark after the first frame, then wait only for what the program draws
// next. A program started twice prints the same line twice, so without --since the
// second run's prompt is indistinguishable from the first run's leftovers (issue #126).
func TestMarkAndWaitSinceSeeOnlyNewText(t *testing.T) {
	socket := startDaemon(t)
	base := []string{"--socket", socket, "--name", "app"}
	program := []string{"--size", "80x24", "--", "/bin/sh", "-c", "echo ready; read l; echo ready; sleep 5"}

	if code, _, errOut := run(append([]string{"open"}, append(base, program...)...), ""); code != 0 {
		t.Fatalf("open: exit %d, stderr %s", code, errOut)
	}
	if code, _, errOut := run(append([]string{"wait"}, append(base, "--text", "ready", "--timeout", "5s")...), ""); code != 0 {
		t.Fatalf("wait: exit %d, stderr %s", code, errOut)
	}

	// Before any mark, the narrowing is refused rather than widened to the whole screen.
	if code, _, errOut := run(append([]string{"wait"}, append(base, "--text", "ready", "--since", "--timeout", "200ms")...), ""); code != daemon.CodeFailure || errOut == "" {
		t.Errorf("wait --since with no mark: exit %d, stderr %q, want %d and a message", code, errOut, daemon.CodeFailure)
	}

	code, out, errOut := run(append([]string{"mark"}, base...), "")
	if code != 0 || !strings.Contains(out, "marked app at generation") {
		t.Fatalf("mark: exit %d, output %q, stderr %q", code, out, errOut)
	}

	// The first copy is still on screen: the plain wait sees it, the narrowed one must not.
	if code, _, _ := run(append([]string{"wait"}, append(base, "--text", "ready", "--timeout", "300ms")...), ""); code != 0 {
		t.Errorf("a plain wait stopped reading the whole screen: exit %d", code)
	}
	if code, _, _ := run(append([]string{"wait"}, append(base, "--text", "ready", "--since", "--timeout", "300ms")...), ""); code != daemon.CodeTimeout {
		t.Errorf("wait --since on text from before the mark: exit %d, want %d", code, daemon.CodeTimeout)
	}

	// Printing it again satisfies the narrowed wait.
	if code, _, errOut := run(append([]string{"send"}, append(base, "--text", "again", "--key", "enter")...), ""); code != 0 {
		t.Fatalf("send: exit %d, stderr %s", code, errOut)
	}
	if code, _, errOut := run(append([]string{"wait"}, append(base, "--text", "ready", "--since", "--timeout", "5s")...), ""); code != 0 {
		t.Fatalf("wait --since after the reprint: exit %d, stderr %s", code, errOut)
	}
	run(append([]string{"close"}, base...), "")
}
