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

// TestSessionEnvOverridesWin is the rule the parity run surfaced: a caller that pins
// TERM must win over the value this process happens to have. An agent's shell often
// has TERM=dumb, and a TUI that reads it paints no colour at all.
func TestSessionEnvOverridesWin(t *testing.T) {
	t.Setenv("TERM", "dumb")

	s, out := shell(t, Size{Cols: 40, Rows: 3}, "echo term=$TERM")
	defer s.Close()
	if !strings.Contains(out, "term=dumb") {
		t.Errorf("without an override the inherited TERM should be kept, got %q", out)
	}

	// The caller's value must win, and must be the child's only TERM entry: Go
	// passes duplicates through and the child reads the first.
	s2, out2 := startEnvTest(t, []string{"TERM=xterm-256color", "COLORTERM=truecolor"})
	defer s2.Close()
	if !strings.Contains(out2, "term=xterm-256color") || strings.Contains(out2, "term=dumb") {
		t.Errorf("the caller's TERM must be what the child sees, got %q", out2)
	}
	if !strings.Contains(out2, "colorterm=truecolor") {
		t.Errorf("the caller's COLORTERM did not reach the child: %q", out2)
	}
}

// startEnvTest runs a shell with an explicit environment.
func startEnvTest(t *testing.T, env []string) (*Session, string) {
	t.Helper()
	s, err := Start(Options{Args: []string{"/bin/sh", "-c", "echo term=$TERM; echo colorterm=$COLORTERM"}, Env: env, Size: Size{Cols: 40, Rows: 3}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	data, _ := io.ReadAll(s)
	return s, string(data)
}

// TestSessionEnvRemovesAVariable covers the escape hatch an agent's shell needs:
// NO_COLOR=1 in the parent must be removable, or every style in the child is off.
func TestSessionEnvRemovesAVariable(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	s, out := shell(t, Size{Cols: 40, Rows: 3}, "echo no_color=${NO_COLOR:-unset}")
	defer s.Close()
	if !strings.Contains(out, "no_color=1") {
		t.Errorf("the parent's NO_COLOR should be inherited by default, got %q", out)
	}

	s2, err := Start(Options{
		Args: []string{"/bin/sh", "-c", "echo no_color=${NO_COLOR:-unset}; echo term=$TERM"},
		Env:  []string{"NO_COLOR", "TERM=xterm-256color"},
		Size: Size{Cols: 60, Rows: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	data, _ := io.ReadAll(s2)
	got := string(data)
	if strings.Contains(got, "no_color=1") {
		t.Errorf("a bare NO_COLOR must remove the variable, got %q", got)
	}
	if !strings.Contains(got, "no_color=unset") || !strings.Contains(got, "term=xterm-256color") {
		t.Errorf("environment after the removal = %q", got)
	}
}

// TestTerminalEnvPinsTheColourDecision: the helper a hermetic run uses must give a
// program a colour terminal regardless of what the caller's shell says.
func TestTerminalEnvPinsTheColourDecision(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR", "0")

	// The list is applied *on top of* this process's environment, which is how a
	// caller uses it — so what matters is the effective environment.
	effective := strings.Join(environment(TerminalEnv("/tmp/home"), Size{Cols: 80, Rows: 24}), "\n")
	for _, forbidden := range []string{"TERM=dumb", "NO_COLOR", "CLICOLOR="} {
		if strings.Contains(effective, forbidden) {
			t.Errorf("the effective environment still carries %q:\n%s", forbidden, effective)
		}
	}
	for _, want := range []string{"TERM=xterm-256color", "COLORTERM=truecolor", "CLICOLOR_FORCE=1", "HOME=/tmp/home"} {
		if !strings.Contains(effective, want) {
			t.Errorf("the effective environment is missing %q:\n%s", want, effective)
		}
	}
}
