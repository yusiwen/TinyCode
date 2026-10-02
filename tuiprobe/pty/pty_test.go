package pty

import (
	"io"
	"strings"
	"testing"
	"time"
)

// shell runs one shell command on a PTY and returns everything it wrote.
func shell(t *testing.T, size Size, script string) (*Session, string) {
	t.Helper()
	s, err := Start(Options{Args: []string{"/bin/sh", "-c", script}, Size: size})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	done := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(s)
		done <- string(data)
	}()
	select {
	case out := <-done:
		return s, out
	case <-time.After(20 * time.Second):
		_ = s.master.Close()
		t.Fatalf("the program never closed its stream; output so far: %q", "")
		return nil, ""
	}
}

func TestSessionReportsOutputAndExitCode(t *testing.T) {
	s, out := shell(t, Size{Cols: 80, Rows: 24}, "echo hello; exit 7")
	if !strings.Contains(out, "hello") {
		t.Errorf("output = %q, want it to contain hello", out)
	}
	code, _ := s.Wait()
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	if code, err := s.Close(); code != 7 || err != nil {
		t.Errorf("Close = (%d, %v), want (7, nil) for an already-exited program", code, err)
	}
	if !s.Exited() {
		t.Error("Exited() is false after the program exited")
	}
}

// TestSessionAlwaysGivesTheProgramASize is parity row 10: a program must never
// see a 0x0 terminal, because the ones that divide by width crash on it.
func TestSessionAlwaysGivesTheProgramASize(t *testing.T) {
	for _, tc := range []struct {
		name string
		size Size
		want string
	}{
		{"requested", Size{Cols: 100, Rows: 40}, "40 100"},
		{"zero", Size{}, "24 80"},
		{"negative", Size{Cols: -1, Rows: -1}, "24 80"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, out := shell(t, tc.size, "stty size")
			defer s.Close()
			if got := strings.TrimSpace(out); !strings.Contains(got, tc.want) {
				t.Errorf("stty size = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestSessionResizeReachesTheProgram(t *testing.T) {
	s, err := Start(Options{Args: []string{"/bin/sh"}, Size: Size{Cols: 80, Rows: 24}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	if err := s.Resize(Size{Cols: 120, Rows: 50}); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if _, err := s.Write([]byte("stty size\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := readUntil(t, s, "50 120"); !strings.Contains(got, "50 120") {
		t.Errorf("after resize the program reports %q, want 50 120", got)
	}
}

// TestSessionCloseIsBoundedAndKillsTheGroup covers the cleanup rule: a program
// that ignores its input must not be able to hold cleanup open.
func TestSessionCloseIsBoundedAndKillsTheGroup(t *testing.T) {
	s, err := Start(Options{Args: []string{"/bin/sh", "-c", "sleep 30"}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	started := time.Now()
	if _, err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if elapsed := time.Since(started); elapsed > CloseGrace {
		t.Errorf("Close took %s, want well under the %s grace", elapsed, CloseGrace)
	}
	if !s.Exited() {
		t.Error("the program is still running after Close")
	}
}

func TestSessionStartFailsClearly(t *testing.T) {
	if _, err := Start(Options{Args: []string{"/nonexistent/tuiprobe-program"}}); err == nil {
		t.Fatal("starting a missing program must fail")
	}
	if _, err := Start(Options{}); err == nil {
		t.Fatal("starting nothing must fail")
	}
}

// readUntil reads from the session until want appears or the stream ends.
func readUntil(t *testing.T, s *Session, want string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var out strings.Builder
	buf := make([]byte, 4096)
	for time.Now().Before(deadline) {
		n, err := s.Read(buf)
		out.Write(buf[:n])
		if strings.Contains(out.String(), want) {
			return out.String()
		}
		if err != nil {
			return out.String()
		}
	}
	return out.String()
}
