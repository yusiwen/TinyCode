package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// startServer runs a daemon on a short socket path (unix sockets have a length
// limit, and t.TempDir is too long) and stops it when the test ends.
func startServer(t *testing.T) (*Client, string) {
	t.Helper()
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("tpd-%d-%d.sock", os.Getpid(), time.Now().UnixNano()%100000))
	srv := New(socket, time.Minute)
	ln, err := srv.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		srv.Stop()
		_ = ln.Close()
	})
	// The daemon is in-process: the client must not try to launch one.
	return &Client{Socket: socket, StartDaemon: func(string) error { return nil }}, socket
}

// call runs one request and fails the test on an unexpected refusal.
func call(t *testing.T, c *Client, req Request) Response {
	t.Helper()
	resp, err := c.Call(req)
	if err != nil {
		t.Fatalf("%s: %v", req.Cmd, err)
	}
	if !resp.OK {
		t.Fatalf("%s: %s", req.Cmd, resp.Error)
	}
	return resp
}

// TestSessionLifecycle drives a program the way an agent does: one request at a
// time, each of them able to be the only thing running.
func TestSessionLifecycle(t *testing.T) {
	c, _ := startServer(t)

	open := call(t, c, Request{
		Cmd:  "open",
		Name: "app",
		Args: []string{"/bin/sh", "-c", "echo ready; read line; echo got:$line; exit 5"},
		Cols: 100, Rows: 30,
	})
	if open.Pid == 0 || open.Cols != 100 || open.Rows != 30 {
		t.Fatalf("open = %+v, want a pid and 100x30", open)
	}

	if _, err := c.Call(Request{Cmd: "wait", Name: "app", Pattern: "ready", Timeout: "5s"}); err != nil {
		t.Fatalf("wait: %v", err)
	}

	screen := call(t, c, Request{Cmd: "text", Name: "app"})
	if !strings.Contains(screen.Text, "ready") {
		t.Errorf("screen = %q, want it to contain ready", screen.Text)
	}
	if screen.Cols != 100 || screen.Rows != 30 {
		t.Errorf("screen reports %dx%d, want 100x30", screen.Cols, screen.Rows)
	}

	// A keystroke: text plus Enter, which is what a line-oriented program needs.
	call(t, c, Request{Cmd: "send", Name: "app", Text: "hi", Keys: []string{"enter"}})
	call(t, c, Request{Cmd: "wait", Name: "app", Pattern: "got:hi", Timeout: "5s"})

	if trace := call(t, c, Request{Cmd: "trace", Name: "app", N: 64}).Trace; trace == "" {
		t.Error("trace is empty; the raw stream should be retained")
	}

	sessions := call(t, c, Request{Cmd: "sessions"})
	if sessions.SessionCnt != 1 || len(sessions.Sessions) != 1 || sessions.Sessions[0].Name != "app" {
		t.Fatalf("sessions = %+v, want one session named app", sessions)
	}

	// "It stopped printing" and "it exited" are different moments: ask for the exit,
	// then read the code. Asking too early used to kill the program and report -1 —
	// a flake that only showed on a loaded CI runner.
	exited := call(t, c, Request{Cmd: "wait-exit", Name: "app", Timeout: "10s"})
	if !exited.Exited || !exited.HasExited || exited.ExitCode != 5 {
		t.Errorf("wait-exit = %+v, want the program's own code 5", exited)
	}
	if after := call(t, c, Request{Cmd: "sessions"}); after.SessionCnt != 0 {
		t.Errorf("session count after wait-exit = %d, want 0", after.SessionCnt)
	}
}

func TestRefusalsAreClassified(t *testing.T) {
	c, _ := startServer(t)

	resp, err := c.Call(Request{Cmd: "text", Name: "nope"})
	if err != nil || resp.OK || resp.Code != CodeNoSuch {
		t.Fatalf("text on a missing session = (%+v, %v), want a CodeNoSuch refusal", resp, err)
	}

	if resp, _ := c.Call(Request{Cmd: "open", Name: "x"}); resp.OK {
		t.Error("open without a command must be refused")
	}
	if resp, _ := c.Call(Request{Cmd: "wait", Name: "x"}); resp.OK {
		t.Error("wait without --text or --stable must be refused")
	}
	if resp, _ := c.Call(Request{Cmd: "nonsense"}); resp.OK {
		t.Error("an unknown command must be refused")
	}

	call(t, c, Request{Cmd: "open", Name: "dup", Args: []string{"/bin/sh", "-c", "sleep 5"}})
	if resp, _ := c.Call(Request{Cmd: "open", Name: "dup", Args: []string{"/bin/sh"}}); resp.OK {
		t.Error("opening the same name twice must be refused")
	}
}

// TestWaitTimeoutIsReportedAsATimeout covers the exit-code contract: a wait that
// ran out of time is CodeTimeout, not a generic failure, so a caller can tell the
// two apart without parsing the message.
func TestWaitTimeoutIsReportedAsATimeout(t *testing.T) {
	c, _ := startServer(t)
	call(t, c, Request{Cmd: "open", Name: "idle", Args: []string{"/bin/sh", "-c", "sleep 30"}})

	resp, _ := c.Call(Request{Cmd: "wait", Name: "idle", Pattern: "never appears", Timeout: "150ms"})
	if resp.OK || resp.Code != CodeTimeout || resp.Stage != "wait" {
		t.Fatalf("wait timeout = %+v, want CodeTimeout at stage wait", resp)
	}
}

func TestResizeIsReportedBack(t *testing.T) {
	c, _ := startServer(t)
	call(t, c, Request{Cmd: "open", Name: "app", Args: []string{"/bin/sh", "-c", "sleep 30"}, Cols: 80, Rows: 24})

	resized := call(t, c, Request{Cmd: "resize", Name: "app", Cols: 132, Rows: 43})
	if resized.Cols != 132 || resized.Rows != 43 {
		t.Errorf("resize = %+v, want 132x43", resized)
	}
}

// TestListenReplacesAStaleSocket covers the crash case: a socket file left behind
// by a dead daemon must not block a new one, while a live daemon must.
func TestListenReplacesAStaleSocket(t *testing.T) {
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("tpd-stale-%d.sock", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(socket) })

	// A plain file where the socket should be is the interrupted-shutdown case.
	if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := New(socket, time.Minute)
	ln, err := srv.Listen()
	if err != nil {
		t.Fatalf("Listen over a stale file: %v", err)
	}
	defer func() { srv.Stop(); _ = ln.Close() }()

	// A second daemon must refuse while the first is listening.
	other := New(socket, time.Minute)
	if ln2, err := other.Listen(); err == nil {
		_ = ln2.Close()
		t.Fatal("a second daemon must not take over a live socket")
	}
}

// TestClientStartsADaemon exercises the auto-start path with a server that is not
// running yet: the injected starter stands in for the detached process.
func TestClientStartsADaemon(t *testing.T) {
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("tpd-start-%d.sock", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(socket) })

	var started bool
	srv := New(socket, time.Minute)
	client := &Client{
		Socket: socket,
		StartDaemon: func(path string) error {
			started = true
			ln, err := srv.Listen()
			if err != nil {
				return err
			}
			go func() { _ = srv.Serve(ln) }()
			t.Cleanup(func() { srv.Stop(); _ = ln.Close() })
			return nil
		},
	}

	resp, err := client.Call(Request{Cmd: "ping"})
	if err != nil || !resp.OK {
		t.Fatalf("Call after auto-start = (%+v, %v)", resp, err)
	}
	if !started {
		t.Error("the client did not start a daemon")
	}
}

// TestIdleExitStopsTheDaemon pins the "nothing lingers" promise: with no sessions
// and no traffic, the daemon exits by itself.
func TestIdleExitStopsTheDaemon(t *testing.T) {
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("tpd-idle-%d.sock", os.Getpid()))
	srv := New(socket, 50*time.Millisecond) // small TTL: the reap interval follows it
	ln, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve after idle = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		_ = ln.Close()
		t.Fatal("the daemon did not exit when idle")
	}
	if _, err := net.DialTimeout("unix", socket, 100*time.Millisecond); err == nil {
		t.Error("the socket is still accepting after the idle exit")
	}
}
