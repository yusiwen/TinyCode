// Package session drives one program on a PTY and keeps the screen it draws.
//
// It is the engine a person, a test or an agent talks to: send keys, wait for the
// screen to say something, read the screen as text, ANSI or HTML, and get the exit
// code when the program is done. Every step is bounded, and a failure names the
// step it happened in.
package session

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yusiwen/TinyCode/tuiprobe/pty"
	"github.com/yusiwen/TinyCode/tuiprobe/screen"
)

// DefaultTraceLimit is how many bytes of the raw stream are kept for diagnosis.
const DefaultTraceLimit = 256 << 10

// PollInterval is how often the wait helpers look at the screen. It bounds the
// cost of a wait and the latency of noticing a change.
var PollInterval = 20 * time.Millisecond

// Options describe the program to run.
type Options struct {
	Args []string
	Dir  string
	Env  []string
	Size pty.Size
	// TraceLimit bounds the retained raw stream; zero means DefaultTraceLimit.
	TraceLimit int
}

// Session is a running program, its terminal and its screen.
type Session struct {
	program *pty.Session
	size    pty.Size

	mu         sync.Mutex
	terminal   *screen.Buffer
	trace      *ring
	changedAt  time.Time
	readErr    error
	readClosed bool
}

// Start launches the program and begins replaying its stream into a screen.
func Start(opts Options) (*Session, error) {
	size := opts.Size
	if size.Cols <= 0 || size.Rows <= 0 {
		size = pty.DefaultSize
	}
	limit := opts.TraceLimit
	if limit <= 0 {
		limit = DefaultTraceLimit
	}

	program, err := pty.Start(pty.Options{Args: opts.Args, Dir: opts.Dir, Env: opts.Env, Size: size})
	if err != nil {
		return nil, err
	}

	s := &Session{
		program:   program,
		size:      size,
		terminal:  screen.New(size.Cols, size.Rows),
		trace:     newRing(limit),
		changedAt: time.Now(),
	}
	go s.replay()
	return s, nil
}

// replay feeds everything the program writes into the screen and the trace.
func (s *Session) replay() {
	buf := make([]byte, 32<<10)
	for {
		n, err := s.program.Read(buf)
		if n > 0 {
			s.mu.Lock()
			s.trace.Write(buf[:n])
			_, _ = s.terminal.Write(buf[:n])
			s.changedAt = time.Now()
			s.mu.Unlock()
		}
		if err != nil {
			s.mu.Lock()
			if err != io.EOF {
				s.readErr = err
			}
			s.readClosed = true
			s.changedAt = time.Now()
			s.mu.Unlock()
			return
		}
	}
}

// Send writes keys to the program. Each argument is either a key name ("enter",
// "ctrl+c", "alt+left", "f5") or literal text; use Write for bytes that must be
// taken literally.
func (s *Session) Send(keys ...string) error {
	for _, key := range keys {
		if seq, ok := Key(key); ok {
			if _, err := s.program.Write(seq); err != nil {
				return fmt.Errorf("session: send %s: %w", key, err)
			}
			continue
		}
		if _, err := s.program.Write([]byte(key)); err != nil {
			return fmt.Errorf("session: send text: %w", err)
		}
	}
	return nil
}

// Write sends raw bytes to the program.
func (s *Session) Write(p []byte) error {
	if _, err := s.program.Write(p); err != nil {
		return fmt.Errorf("session: write: %w", err)
	}
	return nil
}

// Resize changes the terminal geometry and the screen that mirrors it.
func (s *Session) Resize(size pty.Size) error {
	if size.Cols <= 0 || size.Rows <= 0 {
		size = pty.DefaultSize
	}
	if err := s.program.Resize(size); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.size = size
	s.terminal = screen.New(size.Cols, size.Rows)
	s.changedAt = time.Now()
	return nil
}

// Size is the terminal geometry.
func (s *Session) Size() pty.Size {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.size
}

// Text is the screen as plain text, one line per row, trailing blanks trimmed.
func (s *Session) Text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal.String()
}

// ANSI is the screen with its styling encoded as escape sequences.
func (s *Session) ANSI() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal.Render()
}

// HTML is the screen as styled markup, one element per cell.
func (s *Session) HTML() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal.HTML()
}

// Trace is the tail of the raw stream the program wrote, escapes included: what
// the screen no longer shows is often what explains a failure.
func (s *Session) Trace(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.trace.Tail(n))
}

// Pid is the program's process id.
func (s *Session) Pid() int { return s.program.Pid() }

// Exited reports whether the program has exited.
func (s *Session) Exited() bool { return s.program.Exited() }

// Wait blocks until the program exits and returns its exit code.
func (s *Session) Wait() (int, error) { return s.program.Wait() }

// Close reaps the program and returns its exit code.
func (s *Session) Close() (int, error) { return s.program.Close() }

// WaitText waits until the screen matches pattern.
//
// The failure names the step and shows what the screen actually said, so a
// timeout is a diagnosis rather than a shrug.
func (s *Session) WaitText(pattern string, timeout time.Duration) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("session: wait for text: bad pattern %q: %w", pattern, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		if re.MatchString(s.Text()) {
			return nil
		}
		if s.Exited() {
			return fmt.Errorf("session: wait for text %q: the program exited first; screen:\n%s", pattern, s.Text())
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("session: wait for text %q: timed out after %s; screen:\n%s", pattern, timeout, s.Text())
		}
		time.Sleep(PollInterval)
	}
}

// WaitStable waits until the screen has not changed for quiet, which is how a
// caller waits for an animation to settle instead of guessing a sleep.
func (s *Session) WaitStable(quiet, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		s.mu.Lock()
		last := s.changedAt
		s.mu.Unlock()

		if time.Since(last) >= quiet {
			return nil
		}
		if s.Exited() {
			return fmt.Errorf("session: wait for a stable screen: the program exited first; screen:\n%s", s.Text())
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("session: wait for a stable screen: still changing after %s; screen:\n%s", timeout, s.Text())
		}
		time.Sleep(PollInterval)
	}
}

// ring keeps the tail of a stream without growing without bound.
type ring struct {
	data []byte
	max  int
}

func newRing(max int) *ring { return &ring{max: max} }

func (r *ring) Write(p []byte) {
	r.data = append(r.data, p...)
	if len(r.data) > r.max {
		r.data = append([]byte(nil), r.data[len(r.data)-r.max:]...)
	}
}

func (r *ring) Tail(n int) []byte {
	if n <= 0 || n > len(r.data) {
		n = len(r.data)
	}
	return r.data[len(r.data)-n:]
}

// TrimSummary is the one-line form of a screen, for logs and error messages.
func TrimSummary(text string, maxLines int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	return strings.Join(lines, " | ")
}
