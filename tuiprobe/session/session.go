// Package session drives one program on a PTY and keeps the screen it draws.
//
// It is the engine a person, a test or an agent talks to: send keys, wait for the
// screen to say something, read the screen as text, ANSI or HTML, and get the exit
// code when the program is done. Every step is bounded, and a failure names the
// step it happened in.
package session

import (
	"bytes"
	"fmt"
	"io"
	"os"
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
	// Background is the colour reported for OSC 11 ("what is your background?"), as
	// #rrggbb; empty means DefaultBackground. A program uses it to choose a dark or
	// light theme, so it should match what the renderers draw.
	Background string
	// NoQueryAnswers disables answering terminal queries (TUIPROBE_ANSWER_QUERIES=0
	// does the same). A real terminal answers; this exists for the test that proves a
	// program copes when one does not.
	NoQueryAnswers bool
}

// DefaultBackground is what OSC 11 is answered with when Options.Background is empty.
// It matches the renderers' default page colour.
const DefaultBackground = "#1c1c1c"

// queryWindow is how much of the previous chunk is kept when looking for a query that
// straddles a read boundary.
const queryWindow = 16

// Session is a running program, its terminal and its screen.
type Session struct {
	program *pty.Session
	size    pty.Size

	mu             sync.Mutex
	queryTail      []byte
	background     string
	noQueryAnswers bool
	terminal       *screen.Buffer
	trace          *ring
	changedAt      time.Time
	readErr        error
	readClosed     bool
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

	background := opts.Background
	noAnswers := opts.NoQueryAnswers || os.Getenv("TUIPROBE_ANSWER_QUERIES") == "0"
	s := &Session{
		program:        program,
		size:           size,
		background:     background,
		noQueryAnswers: noAnswers,
		terminal:       screen.New(size.Cols, size.Rows),
		trace:          newRing(limit),
		changedAt:      time.Now(),
	}
	go s.replay()
	return s, nil
}

// queryReplies answers the terminal queries a program asks before it will paint.
//
// TinyCode writes OSC 11 ("what is your background?") and CSI 6n ("where is the
// cursor?") at startup and then paints nothing until they time out — measured at
// roughly five seconds, which is how a `sleep 3` capture saw an empty screen and a
// `sleep 8` capture saw the welcome screen (issue #76). A real terminal answers both,
// so an emulator that stays silent is not being faithful, it is being slow.
//
// Called with s.mu held.
func (s *Session) queryReplies(chunk []byte) [][]byte {
	if s.noQueryAnswers {
		return nil
	}
	scan := chunk
	if len(s.queryTail) > 0 {
		scan = append(append([]byte(nil), s.queryTail...), chunk...)
	}
	// Only the tail is kept: a query is short and arrives in one write almost always,
	// but a boundary can fall inside one.
	keep := queryWindow
	if len(chunk) < keep {
		keep = len(chunk)
	}
	s.queryTail = append(s.queryTail[:0], chunk[len(chunk)-keep:]...)

	var replies [][]byte
	backgroundQuery := []byte("\x1b]11;?")
	if at := bytes.Index(scan, backgroundQuery); at >= 0 && at+len(backgroundQuery) > len(scan)-len(chunk) {
		colour := s.background
		if colour == "" {
			colour = DefaultBackground
		}
		replies = append(replies, []byte("\x1b]11;"+oscRGB(colour)+"\x1b\\"))
	}
	cursorQuery := []byte("\x1b[6n")
	if at := bytes.Index(scan, cursorQuery); at >= 0 && at+len(cursorQuery) > len(scan)-len(chunk) {
		col, row := s.terminal.Cursor()
		replies = append(replies, []byte(fmt.Sprintf("\x1b[%d;%dR", row+1, col+1)))
	}
	return replies
}

// oscRGB turns #rrggbb into the rgb:rrrr/gggg/bbbb form OSC 11 expects.
func oscRGB(hex string) string {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) != 6 {
		hex = strings.TrimPrefix(DefaultBackground, "#")
	}
	// Four hex digits per channel: what a real terminal answers with, and what
	// termenv and friends expect to parse.
	return fmt.Sprintf("rgb:%s%s/%s%s/%s%s", hex[0:2], hex[0:2], hex[2:4], hex[2:4], hex[4:6], hex[4:6])
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
			reply := s.queryReplies(buf[:n])
			s.mu.Unlock()
			// The replies go back to the program's terminal, outside the lock: a
			// program that is waiting for one is not reading anything else.
			for _, r := range reply {
				_, _ = s.program.Write(r)
			}
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
			// A program that printed the answer and left *has* satisfied the wait, and
			// the reader may still be applying its last bytes when the process ends —
			// the exit flag and the final screen are not ordered. Look once more,
			// briefly, before reporting failure: a CI run of the CLI tests found
			// "got:hello" on the screen and this error at the same time.
			if s.awaitFinal(re, 500*time.Millisecond) {
				return nil
			}
			return fmt.Errorf("session: wait for text %q: the program exited first; screen:\n%s", pattern, s.Text())
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("session: wait for text %q: %w after %s; screen:\n%s", pattern, ErrStageTimeout, timeout, s.Text())
		}
		time.Sleep(PollInterval)
	}
}

// awaitFinal looks for the pattern in the final output of a program that has already
// exited, giving the reader a bounded moment to finish applying the last bytes.
func (s *Session) awaitFinal(re *regexp.Regexp, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	for {
		if re.MatchString(s.Text()) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
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
			// Nothing can change any more, so the screen is stable by definition.
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("session: wait for a stable screen: %w, still changing after %s; screen:\n%s", ErrStageTimeout, timeout, s.Text())
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
