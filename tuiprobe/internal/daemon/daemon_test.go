package daemon

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// TestIdleExitClosesALiveSession is the regression guard for the leak this daemon
// used to have: its idle rule demanded an empty session map, so a daemon holding a
// session never expired. A client that was killed, or that simply walked away,
// then left the program under test — and every child it had spawned — running for
// as long as the machine was up. Measured before the fix, on this repository's own
// TUI targets: a daemon with `--ttl 5s` and one live child was still there 30
// seconds later, and two such pairs had run for six and five days.
func TestIdleExitClosesALiveSession(t *testing.T) {
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("tpd-live-%d.sock", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(socket) })

	srv := New(socket, 200*time.Millisecond)
	ln, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	t.Cleanup(func() { srv.Stop(); _ = ln.Close() })

	c := &Client{Socket: socket, StartDaemon: func(string) error { return nil }}
	opened := call(t, c, Request{
		Cmd:  "open",
		Name: "leak",
		Args: []string{"/bin/sh", "-c", "while :; do sleep 0.2; done"},
	})
	if err := syscall.Kill(opened.Pid, 0); err != nil {
		t.Fatalf("the program under test (pid %d) is not running: %v", opened.Pid, err)
	}

	// Nobody will talk to this daemon again: the TTL is the only thing that can
	// end it, and it must end it even though a session is still open.
	select {
	case err := <-done:
		t.Fatalf("Serve returned before the TTL elapsed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve after the idle exit = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon outlived its TTL while holding a live session")
	}

	// It has to take the program with it: a daemon that exits without reaping its
	// PTY children leaves exactly the orphan this guards against.
	if err := syscall.Kill(opened.Pid, 0); err == nil {
		_ = syscall.Kill(opened.Pid, syscall.SIGKILL)
		t.Errorf("the program under test (pid %d) survived the daemon that held it", opened.Pid)
	}
}

// TestReleaseSparesABusyDaemonAndStopsADrainedOne pins the polite exit a client
// uses when it is the one that started a daemon (issue #141): a run must not leave
// its daemon behind, and it must never end a daemon that another client is using.
func TestReleaseSparesABusyDaemonAndStopsADrainedOne(t *testing.T) {
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("tpd-release-%d.sock", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(socket) })

	srv := New(socket, time.Minute)
	ln, err := srv.Listen()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	t.Cleanup(func() { srv.Stop(); _ = ln.Close() })

	c := &Client{Socket: socket, StartDaemon: func(string) error { return nil }}
	call(t, c, Request{Cmd: "open", Name: "app", Args: []string{"/bin/sh", "-c", "sleep 30"}})

	// A client that did not start this daemon does not release it.
	c.Release()
	select {
	case err := <-done:
		t.Fatalf("a client that did not start the daemon ended it: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// One that did start it still cannot end a daemon that holds a session: the
	// release is refused, and the session is still there afterwards.
	c.Started = true
	c.Release()
	if sessions := call(t, c, Request{Cmd: "sessions"}); sessions.SessionCnt != 1 {
		t.Fatalf("session count after a refused release = %d, want 1", sessions.SessionCnt)
	}

	// With the session closed the daemon holds nothing, and the release takes it.
	call(t, c, Request{Cmd: "close", Name: "app"})
	c.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve after the release = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the released daemon is still running with nothing to hold")
	}
	if _, err := net.DialTimeout("unix", socket, 100*time.Millisecond); err == nil {
		t.Error("the socket is still accepting after the release")
	}
}

// TestEveryResponseCarriesTheLiveCount is the guard for issue #146. SessionCnt has
// no omitempty so that a caller can read it on every answer, and it used to be
// filled only by `sessions`: `open --json` reported `"sessionCount": 0` on the very
// response that had just added the session.
func TestEveryResponseCarriesTheLiveCount(t *testing.T) {
	c, _ := startServer(t)
	live := []string{"/bin/sh", "-c", "echo up; sleep 30"}

	if opened := call(t, c, Request{Cmd: "open", Name: "a", Args: live}); opened.SessionCnt != 1 {
		t.Errorf("open reported sessionCount %d, want 1", opened.SessionCnt)
	}
	if waited := call(t, c, Request{Cmd: "wait", Name: "a", Pattern: "up", Timeout: "5s"}); waited.SessionCnt != 1 {
		t.Errorf("wait reported sessionCount %d, want 1", waited.SessionCnt)
	}
	if screen := call(t, c, Request{Cmd: "text", Name: "a"}); screen.SessionCnt != 1 {
		t.Errorf("text reported sessionCount %d, want 1", screen.SessionCnt)
	}
	call(t, c, Request{Cmd: "open", Name: "b", Args: live})
	if listed := call(t, c, Request{Cmd: "sessions"}); listed.SessionCnt != 2 || len(listed.Sessions) != 2 {
		t.Fatalf("sessions = %+v, want two sessions", listed)
	}

	// The count is the live one at answer time, so it is what the next `sessions`
	// reports too — after a close that removed a session, not before it.
	closed := call(t, c, Request{Cmd: "close", Name: "a"})
	if closed.SessionCnt != 1 {
		t.Errorf("close reported sessionCount %d, want 1", closed.SessionCnt)
	}
	if listed := call(t, c, Request{Cmd: "sessions"}); listed.SessionCnt != closed.SessionCnt {
		t.Errorf("sessions reports %d where close reported %d; they must agree",
			listed.SessionCnt, closed.SessionCnt)
	}

	// A refusal carries it as well: the field describes the daemon, not how many
	// sessions the command managed to touch.
	if refused, _ := c.Call(Request{Cmd: "text", Name: "nope"}); refused.OK || refused.SessionCnt != 1 {
		t.Errorf("a refused text = %+v, want OK=false with sessionCount 1", refused)
	}
}

// TestMarkMakesAWaitSeeOnlyNewText is the daemon-level guard for issue #126: a program
// started twice in one session paints the same frame twice, so a plain `wait --text` on
// the second run is satisfied by the first run's leftovers. A wait carrying Since reads
// only what was drawn after the mark.
func TestMarkMakesAWaitSeeOnlyNewText(t *testing.T) {
	c, _ := startServer(t)
	call(t, c, Request{
		Cmd: "open", Name: "app",
		Args: []string{"/bin/sh", "-c", "printf 'ready\\n'; read l; printf 'ready\\n'; sleep 5"},
	})

	// Two refusals, so neither mistake is quietly widened into a match on the whole
	// screen: no mark yet, and nothing to match on.
	if resp, _ := c.Call(Request{Cmd: "wait", Name: "app", Pattern: "ready", Since: true, Timeout: "100ms"}); resp.OK || resp.Code != CodeFailure {
		t.Fatalf("wait --since without a mark = %+v, want a refusal", resp)
	}
	if resp, _ := c.Call(Request{Cmd: "wait", Name: "app", Since: true, Timeout: "100ms"}); resp.OK || !strings.Contains(resp.Error, "needs --text") {
		t.Fatalf("wait --since without --text = %+v, want a usage refusal", resp)
	}

	call(t, c, Request{Cmd: "wait", Name: "app", Pattern: "ready", Timeout: "5s"})
	marked := call(t, c, Request{Cmd: "mark", Name: "app"})
	if marked.Generation == 0 {
		t.Fatalf("mark = %+v, want the generation it recorded", marked)
	}

	// The stale copy is still on the screen: the plain wait sees it, the since-wait
	// must not.
	call(t, c, Request{Cmd: "wait", Name: "app", Pattern: "ready", Timeout: "1s"})
	if resp, _ := c.Call(Request{Cmd: "wait", Name: "app", Pattern: "ready", Since: true, Timeout: "300ms"}); resp.OK || resp.Code != CodeTimeout {
		t.Fatalf("wait --since on text from before the mark = %+v, want a timeout", resp)
	}

	// Printing it again does satisfy the since-wait.
	call(t, c, Request{Cmd: "send", Name: "app", Text: "again", Keys: []string{"enter"}})
	call(t, c, Request{Cmd: "wait", Name: "app", Pattern: "ready", Since: true, Timeout: "5s"})
}
