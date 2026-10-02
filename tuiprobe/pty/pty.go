// Package pty runs a program on a pseudo-terminal.
//
// A TUI only exists when a terminal renders it: run the same binary with a pipe
// for stdout and it will either refuse, or draw a different, degraded layout. So
// the tool gives every program a real terminal, and this package is that
// terminal — spawn, size, resize, write, read, reap.
//
// It is deliberately small and has no opinion about what the program draws;
// interpreting the bytes is the job of package screen and package session.
//
// Linux and macOS only: the reaping path uses process groups and the EIO-on-exit
// behaviour of a PTY master. Windows would need ConPTY and a separate
// implementation.
package pty

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// Size is a terminal geometry in character cells.
type Size struct {
	Cols, Rows int
}

// DefaultSize is what a program gets when the caller does not ask for a size.
//
// Terminal applications routinely divide by the width or index a buffer by the
// height, so handing them a 0x0 terminal is a crash, not a neutral default: the
// harness this tool grew out of lost a run to exactly that.
var DefaultSize = Size{Cols: 80, Rows: 24}

// CloseGrace bounds how long Close waits for a killed program to be reaped.
// Cleanup that waits forever is how a wedged child turns into a wedged test run.
var CloseGrace = 5 * time.Second

// Options describe the program to run.
type Options struct {
	// Args is the command and its arguments; Args[0] is resolved on PATH.
	Args []string
	// Dir is the working directory, "" for the current one.
	Dir string
	// Env holds extra environment variables (KEY=value). They are appended to the
	// caller's environment, so a later entry wins.
	Env []string
	// Size is the terminal geometry; a zero or negative field falls back to
	// DefaultSize.
	Size Size
}

// Session is a program running on a PTY.
type Session struct {
	master *os.File
	cmd    *exec.Cmd

	waitOnce sync.Once
	waitDone chan struct{}
	exitCode int
	waitErr  error

	closeOnce sync.Once
	closeErr  error
}

// Start launches the program on a fresh PTY of the requested size.
//
// The size is applied before the child runs (creack/pty's StartWithSize), so a
// program that reads the geometry during startup never sees the 0x0 window a
// two-step start produces.
func Start(opts Options) (*Session, error) {
	if len(opts.Args) == 0 {
		return nil, errors.New("pty: no command given")
	}
	size := opts.Size
	if size.Cols <= 0 || size.Rows <= 0 {
		size = DefaultSize
	}

	cmd := exec.Command(opts.Args[0], opts.Args[1:]...)
	cmd.Dir = opts.Dir
	cmd.Env = environment(opts.Env, size)

	master, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(size.Cols), Rows: uint16(size.Rows)})
	if err != nil {
		return nil, fmt.Errorf("pty: start %s: %w", opts.Args[0], err)
	}

	s := &Session{master: master, cmd: cmd, waitDone: make(chan struct{})}
	// Reap as soon as the program exits: a zombie child keeps its process group
	// alive, and Exited() must answer without the caller having called Wait.
	go func() { _, _ = s.Wait() }()
	return s, nil
}

// environment is the child's environment.
//
// Order and duplicates matter here: a caller that passes TERM=xterm-256color must
// win over the value this process happens to have (an agent's TERM=dumb, say), and
// the child must see one entry per key. Go passes duplicates through and a program
// reading the environment gets the first, so the caller's values are applied as
// overrides rather than appended.
func environment(extra []string, size Size) []string {
	var env []string
	set := func(key, value string) {
		prefix := key + "="
		for i, kv := range env {
			if strings.HasPrefix(kv, prefix) {
				env[i] = prefix + value
				return
			}
		}
		env = append(env, prefix+value)
	}

	for _, kv := range os.Environ() {
		if key, value, ok := strings.Cut(kv, "="); ok {
			set(key, value)
		}
	}
	for _, kv := range extra {
		if key, value, ok := strings.Cut(kv, "="); ok {
			set(key, value)
			continue
		}
		// A bare KEY removes the variable: this process's environment is inherited
		// by the child, and an agent's shell often carries NO_COLOR=1, which makes a
		// TUI paint no colour at all. Saying so has to be possible.
		env = removeKey(env, kv)
	}

	// The terminal variables a TUI reads, filled in only where nothing set them:
	// TERM and COLORTERM decide which escape sequences and colours it dares to
	// emit, and LINES/COLUMNS are read by the applications that do not ask the tty.
	if os.Getenv("TERM") == "" {
		set("TERM", "xterm-256color")
	}
	if os.Getenv("COLORTERM") == "" {
		set("COLORTERM", "truecolor")
	}
	set("LINES", fmt.Sprint(size.Rows))
	set("COLUMNS", fmt.Sprint(size.Cols))
	return env
}

// removeKey drops every entry for one variable.
func removeKey(env []string, key string) []string {
	prefix := key + "="
	out := env[:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, prefix) {
			out = append(out, kv)
		}
	}
	return out
}

// TerminalEnv is the environment a terminal application expects, for a caller that
// wants a hermetic run: the variables that decide colour and geometry are pinned and
// the ones that suppress colour are removed.
//
// It exists because inheriting an agent's environment makes a TUI paint in the
// agent's colours rather than a user's — NO_COLOR=1 turns every style off, and
// TERM=dumb is common in a non-interactive shell. The harness had the same helper
// for the same reason.
func TerminalEnv(home string) []string {
	pinned := map[string]bool{
		"HOME": true, "TERM": true, "COLORTERM": true, "NO_COLOR": true,
		"CLICOLOR": true, "CLICOLOR_FORCE": true, "FORCE_COLOR": true,
	}
	// The list is an *override* list — it is applied on top of the process
	// environment — so the variables that suppress colour are named bare, which
	// removes them rather than leaving the caller's value in place.
	env := []string{"NO_COLOR", "CLICOLOR", "FORCE_COLOR"}
	for _, kv := range os.Environ() {
		if key, _, ok := strings.Cut(kv, "="); ok && pinned[key] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home, "TERM=xterm-256color", "COLORTERM=truecolor", "CLICOLOR_FORCE=1")
}

// Pid is the child's process id (and, because the PTY makes it a session leader,
// its process group id).
func (s *Session) Pid() int { return s.cmd.Process.Pid }

// Read reads the program's output stream.
//
// When the program exits, the master reports EIO rather than a clean EOF on both
// Linux and macOS; that is the end of the stream, not an error.
func (s *Session) Read(p []byte) (int, error) {
	n, err := s.master.Read(p)
	if err != nil && (errors.Is(err, syscall.EIO) || errors.Is(err, os.ErrClosed)) {
		return n, io.EOF
	}
	return n, err
}

// Write sends bytes to the program: keystrokes, pastes, whatever the caller has.
func (s *Session) Write(p []byte) (int, error) { return s.master.Write(p) }

// Resize changes the terminal geometry and signals the program (SIGWINCH), which
// is how a TUI learns to re-lay out.
func (s *Session) Resize(size Size) error {
	if size.Cols <= 0 || size.Rows <= 0 {
		size = DefaultSize
	}
	if err := pty.Setsize(s.master, &pty.Winsize{Cols: uint16(size.Cols), Rows: uint16(size.Rows)}); err != nil {
		return fmt.Errorf("pty: resize: %w", err)
	}
	return nil
}

// Wait blocks until the program exits and reports its exit code.
//
// Callers that must not block forever bound it themselves; Close does.
func (s *Session) Wait() (int, error) {
	s.waitOnce.Do(func() {
		err := s.cmd.Wait()
		s.waitErr = err
		switch {
		case err == nil:
			s.exitCode = 0
		default:
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				s.exitCode = exitErr.ExitCode()
			} else {
				s.exitCode = -1
			}
		}
		close(s.waitDone)
	})
	<-s.waitDone
	return s.exitCode, s.waitErr
}

// Close reaps the program and returns its exit code.
//
// The error reports a cleanup failure — the program could not be reaped inside
// CloseGrace, or the terminal could not be closed. The program's own fate is the
// exit code: a program we had to kill reports -1, which is not an error, since
// killing it was the request. Callers that need the raw wait error (a signal, a
// non-zero status) call Wait themselves.
//
// The order matters and is not negotiable: signal the whole process group first,
// then wait, and bound the wait. Waiting before signalling is how a cleanup hangs
// on a child that never reads stdin, and a plain Kill misses grandchildren that
// the program spawned in its own group.
func (s *Session) Close() (int, error) {
	s.closeOnce.Do(func() {
		if s.cmd.Process != nil {
			// The PTY made the child a session leader, so its pid is its process
			// group: signalling the group reaches what the program spawned, not
			// just the program.
			_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
			_ = s.cmd.Process.Kill()
		}
		select {
		case <-s.waitDone:
		case <-time.After(CloseGrace):
			s.closeErr = fmt.Errorf("pty: program %d did not exit within %s", s.Pid(), CloseGrace)
		}
		if err := s.master.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})
	code, _ := s.Wait()
	return code, s.closeErr
}

// Exited reports whether the program has already exited.
func (s *Session) Exited() bool {
	select {
	case <-s.waitDone:
		return true
	default:
		return false
	}
}
