// Package daemon keeps tuiprobe sessions alive between commands.
//
// An agent, or a person, drives a TUI one command at a time: open it, look at the
// screen, press a key, look again. Each of those is a separate process, so the
// session has to live somewhere else — here, in a small daemon reached over a
// unix socket.
//
// The daemon is shared and session-scoped, not forever: the first command that
// needs it starts it, and it ends when the client that started it is done with it
// and its sessions are closed, or after a whole TTL with no command at all. That
// second deadline is what keeps an abandoned daemon from holding a program under
// test — and the children that program spawned — alive for days on a machine
// nobody is watching.
package daemon

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/yusiwen/TinyCode/tuiprobe/pty"
	"github.com/yusiwen/TinyCode/tuiprobe/session"
)

// Exit codes the CLI uses, so a caller can tell the failure classes apart.
const (
	CodeOK      = 0
	CodeFailure = 2 // bad usage, assertion failed, or the daemon refused
	CodeTimeout = 3
	CodeNoSuch  = 4 // no such session
)

// CmdRelease is the request a client that started a daemon sends when it is done
// with it: the daemon stops once it holds no session, and stays if another client
// has attached one in the meantime.
const CmdRelease = "release"

// Request is one command sent to the daemon. Every field is optional except Cmd.
type Request struct {
	Cmd string `json:"cmd"`

	Name string   `json:"name,omitempty"`
	Args []string `json:"args,omitempty"`
	Dir  string   `json:"dir,omitempty"`
	Env  []string `json:"env,omitempty"`
	Cols int      `json:"cols,omitempty"`
	Rows int      `json:"rows,omitempty"`

	Keys []string `json:"keys,omitempty"`
	Text string   `json:"text,omitempty"`
	N    int      `json:"n,omitempty"`

	Pattern string `json:"pattern,omitempty"`
	Stable  string `json:"stable,omitempty"`
	Timeout string `json:"timeout,omitempty"`
	// Since narrows a text wait to what the program has drawn since the last `mark`,
	// which is how a session that starts the same program twice tells the second run
	// from the first run's leftover frame (issue #126).
	Since bool `json:"since,omitempty"`
}

// Response is the daemon's answer. Screen content is only filled in for the
// commands that ask for it, so a key press does not ship a whole screen back.
type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Code  int    `json:"code,omitempty"`
	Stage string `json:"stage,omitempty"`

	Name string `json:"name,omitempty"`
	Pid  int    `json:"pid,omitempty"`
	Cols int    `json:"cols,omitempty"`
	Rows int    `json:"rows,omitempty"`

	Text  string `json:"text,omitempty"`
	ANSI  string `json:"ansi,omitempty"`
	HTML  string `json:"html,omitempty"`
	Trace string `json:"trace,omitempty"`

	CursorRow int  `json:"cursorRow,omitempty"`
	CursorCol int  `json:"cursorCol,omitempty"`
	Exited    bool `json:"exited,omitempty"`
	ExitCode  int  `json:"exitCode,omitempty"`
	HasExited bool `json:"hasExited,omitempty"`
	// Generation is the position `mark` recorded: how many writes of the program's
	// output had been replayed when it was taken (issue #126).
	Generation uint64 `json:"generation,omitempty"`
	// SessionCnt has no omitempty: an empty daemon is a count of zero, not a
	// missing field, and a caller should not have to guess which it is. Every
	// response carries the live count — handle fills it after dispatch — because a
	// zero in an `open` answer would deny the session it just started (issue #146).
	SessionCnt int `json:"sessionCount"`

	Sessions []SessionInfo `json:"sessions,omitempty"`
}

// SessionInfo describes one live session in a listing.
type SessionInfo struct {
	Name    string `json:"name"`
	Pid     int    `json:"pid"`
	Cols    int    `json:"cols"`
	Rows    int    `json:"rows"`
	Exited  bool   `json:"exited"`
	Created string `json:"created"`
}

// DefaultTTL is how long a daemon may go without a single command before it
// closes whatever it still holds and exits.
//
// It is the second half of the "nothing lingers" promise. A daemon that holds no
// session ends with the request that emptied it; a daemon that still holds one is
// what an abandoned client leaves behind, and without this deadline it would keep
// that program under test (and its child processes) alive indefinitely.
var DefaultTTL = 10 * time.Minute

// DefaultSocket is where the daemon listens unless told otherwise.
func DefaultSocket() string {
	dir := os.Getenv("TUIPROBE_SOCKET")
	if dir != "" {
		return dir
	}
	base := os.Getenv("TMPDIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, fmt.Sprintf("tuiprobe-%d.sock", os.Getuid()))
}

// Server owns the sessions.
type Server struct {
	socket string
	ttl    time.Duration
	logf   func(format string, args ...any)

	mu       sync.Mutex
	sessions map[string]*entry
	lastSeen time.Time
	stopped  bool
	// stopAsked records an explicit `stop` request, which ends the daemon even
	// while it still holds sessions.
	stopAsked bool
	// inFlight counts the requests being served right now, so a request that
	// finds the daemon drained never retires it out from under another request
	// that is still starting the session it is about to add.
	inFlight int

	ln net.Listener // set by Serve; closed by retire
}

type entry struct {
	sess    *session.Session
	pid     int
	created time.Time
}

// New creates a server listening on socket. ttl <= 0 means DefaultTTL.
func New(socket string, ttl time.Duration) *Server {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Server{socket: socket, ttl: ttl, sessions: map[string]*entry{}, lastSeen: time.Now()}
}

// SetLogger routes the daemon's own diagnostics somewhere (stderr by default).
func (s *Server) SetLogger(logf func(string, ...any)) { s.logf = logf }

func (s *Server) log(format string, args ...any) {
	if s.logf != nil {
		s.logf(format, args...)
	}
}

// Listen binds the socket, replacing a stale one left by a dead daemon.
func (s *Server) Listen() (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(s.socket), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(s.socket); err == nil {
		// A live daemon owns the socket; a stale file is an interrupted shutdown.
		if conn, err := net.DialTimeout("unix", s.socket, 200*time.Millisecond); err == nil {
			conn.Close()
			return nil, fmt.Errorf("daemon: %s is already in use", s.socket)
		}
		if err := os.Remove(s.socket); err != nil {
			return nil, fmt.Errorf("daemon: remove stale socket: %w", err)
		}
	}
	return net.Listen("unix", s.socket)
}

// Serve accepts connections until the daemon has nothing left to hold.
//
// It returns nil for every deliberate exit — drained, idle, or an explicit stop —
// and the listener's error otherwise, so the process that launched this one can
// tell "done" from "broken".
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	go s.reapIdle()
	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			stopped := s.stopped
			s.mu.Unlock()
			if stopped {
				return nil
			}
			return err
		}
		go s.handle(conn)
	}
}

// Stop closes every session and shuts the daemon down.
func (s *Server) Stop() {
	s.mu.Lock()
	s.stopped = true
	pending := make([]*session.Session, 0, len(s.sessions))
	for _, e := range s.sessions {
		pending = append(pending, e.sess)
	}
	s.sessions = map[string]*entry{}
	s.mu.Unlock()

	for _, sess := range pending {
		_, _ = sess.Close()
	}
	_ = os.Remove(s.socket)
}

// retire is the single exit door: close what the daemon holds, drop the socket
// and stop accepting, so every reason to stop — the last session closed, no
// command for the whole TTL, an explicit request — ends the process the same way,
// with the PTY children signalled before the socket disappears.
func (s *Server) retire() {
	s.Stop()
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
}

// maybeRetire ends a daemon that a request has left with nothing to hold.
//
// It runs after the response is written, so the caller always gets its answer,
// and only while it is the sole request in flight: a concurrent `open` is about
// to add the session it is still starting, and retiring underneath it would
// orphan the program it just spawned — the very failure this file exists to
// prevent.
func (s *Server) maybeRetire(release bool) {
	s.mu.Lock()
	last := s.inFlight <= 1
	decided := last && (s.stopAsked || (release && len(s.sessions) == 0))
	s.mu.Unlock()
	if decided {
		s.retire()
	}
}

// reapIdle closes the daemon — sessions included — once no command has arrived
// for the whole TTL.
//
// The session count deliberately plays no part. A daemon holding a session whose
// client is gone is exactly the case this deadline exists for: without it, the
// daemon stays alive for as long as the program under test runs, which is what
// left two orphaned daemons and their TUI children burning CPU for days on this
// machine.
func (s *Server) reapIdle() {
	interval := s.ttl / 10
	if interval > time.Minute {
		interval = time.Minute
	}
	if interval < time.Second {
		interval = time.Second
	}
	for {
		time.Sleep(interval)
		s.mu.Lock()
		idle := s.inFlight == 0 && time.Since(s.lastSeen) > s.ttl
		stopped := s.stopped
		s.mu.Unlock()
		if stopped {
			return
		}
		if idle {
			s.log("daemon: no command for %s; closing %s", s.ttl, s.socket)
			s.retire()
			return
		}
	}
}

// handle runs one request. Each connection carries one request and one response,
// which keeps the protocol trivially debuggable with a text socket tool.
func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(bufio.NewReader(conn))
	enc := json.NewEncoder(conn)

	// The request is counted for its whole life, and the idle clock is restarted
	// when it finishes rather than when it arrives: a `wait` that took longer than
	// the TTL must not hand the reaper a daemon that looks abandoned the moment it
	// answers.
	s.mu.Lock()
	s.inFlight++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.lastSeen = time.Now()
		s.mu.Unlock()
	}()

	var req Request
	if err := dec.Decode(&req); err != nil {
		_ = enc.Encode(Response{OK: false, Code: CodeFailure, Error: fmt.Sprintf("daemon: read request: %v", err)})
		return
	}

	resp := s.dispatch(req)
	// Every answer carries the live count, not only `sessions`: the field has no
	// omitempty precisely so a caller can read it everywhere, and a zero in an
	// `open` response would say the opposite of what just happened (issue #146).
	s.mu.Lock()
	resp.SessionCnt = len(s.sessions)
	s.mu.Unlock()
	if err := enc.Encode(resp); err != nil {
		s.log("daemon: write response: %v", err)
	}
	// After the answer is on the wire: a request that leaves the daemon with
	// nothing to hold may take it with it.
	s.maybeRetire(req.Cmd == CmdRelease)
}

func (s *Server) dispatch(req Request) Response {
	switch req.Cmd {
	case "ping":
		return Response{OK: true}
	case "sessions":
		return s.list()
	case "open":
		return s.open(req)
	case "stop":
		// Answered first, then acted on: handle retires the daemon once this
		// response is written, so the caller learns it was heard.
		s.mu.Lock()
		s.stopAsked = true
		s.mu.Unlock()
		return Response{OK: true}
	case CmdRelease:
		// The check happens in handle, after this answer is written: retiring
		// here would close the socket before the caller could read it.
		return Response{OK: true}
	}

	sess, err := s.lookup(req.Name)
	if err != nil {
		return failure(err)
	}
	switch req.Cmd {
	case "send":
		return s.send(sess, req)
	case "text":
		return screenResponse(req.Name, sess, func(r *Response) { r.Text = sess.Text() })
	case "ansi":
		return screenResponse(req.Name, sess, func(r *Response) { r.ANSI = sess.ANSI() })
	case "html":
		return screenResponse(req.Name, sess, func(r *Response) { r.HTML = sess.HTML() })
	case "trace":
		return screenResponse(req.Name, sess, func(r *Response) { r.Trace = sess.Trace(req.N) })
	case "wait":
		return s.wait(req, sess)
	case "mark":
		return s.mark(req, sess)
	case "resize":
		return s.resize(req, sess)
	case "close":
		return s.close(req, sess)
	case "wait-exit":
		return s.waitExit(req, sess)
	default:
		return Response{OK: false, Code: CodeFailure, Error: fmt.Sprintf("daemon: unknown command %q", req.Cmd)}
	}
}

func (s *Server) list() Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	// SessionCnt is filled for every response by handle, so it is not set here.
	resp := Response{OK: true}
	for name, e := range s.sessions {
		resp.Sessions = append(resp.Sessions, SessionInfo{
			Name:    name,
			Pid:     e.pid,
			Cols:    e.sess.Size().Cols,
			Rows:    e.sess.Size().Rows,
			Exited:  e.sess.Exited(),
			Created: e.created.Format(time.RFC3339),
		})
	}
	return resp
}

func (s *Server) open(req Request) Response {
	s.mu.Lock()
	_, exists := s.sessions[req.Name]
	s.mu.Unlock()
	if exists {
		return Response{OK: false, Code: CodeFailure, Error: fmt.Sprintf("daemon: session %q already exists", req.Name)}
	}
	if len(req.Args) == 0 {
		return Response{OK: false, Code: CodeFailure, Error: "daemon: open needs a command"}
	}

	sess, err := session.Start(session.Options{
		Args: req.Args,
		Dir:  req.Dir,
		Env:  req.Env,
		Size: pty.Size{Cols: req.Cols, Rows: req.Rows},
	})
	if err != nil {
		return failure(err)
	}

	s.mu.Lock()
	s.sessions[req.Name] = &entry{sess: sess, pid: sess.Pid(), created: time.Now()}
	s.mu.Unlock()

	size := sess.Size()
	return Response{OK: true, Name: req.Name, Pid: sess.Pid(), Cols: size.Cols, Rows: size.Rows}
}

// send returns metadata only: the caller that wants the screen asks for it, so a
// keystroke does not ship a whole screen over the socket.
func (s *Server) send(sess *session.Session, req Request) Response {
	if req.Text != "" {
		if err := sess.Send(req.Text); err != nil {
			return failure(err)
		}
	}
	if len(req.Keys) > 0 {
		if err := sess.Send(req.Keys...); err != nil {
			return failure(err)
		}
	}
	return screenResponse(req.Name, sess, nil)
}

// mark records where the program's output has reached and reports that position.
//
// It exists because a screen cannot say *when* its text was drawn: a program started
// twice in one session paints the same frame twice, and a `wait --text` on the second
// run is satisfied by the first run's leftovers. A wait carrying Since only reads the
// cells written after this mark (issue #126).
func (s *Server) mark(req Request, sess *session.Session) Response {
	return screenResponse(req.Name, sess, func(r *Response) { r.Generation = sess.Mark() })
}

func (s *Server) wait(req Request, sess *session.Session) Response {
	timeout := 10 * time.Second
	if req.Timeout != "" {
		parsed, err := time.ParseDuration(req.Timeout)
		if err != nil {
			return Response{OK: false, Code: CodeFailure, Error: fmt.Sprintf("daemon: bad timeout %q: %v", req.Timeout, err)}
		}
		timeout = parsed
	}

	var err error
	switch {
	case req.Since && req.Pattern == "":
		return Response{OK: false, Code: CodeFailure, Error: "daemon: wait --since needs --text"}
	case req.Pattern != "":
		if req.Since {
			err = sess.WaitTextSince(req.Pattern, timeout)
		} else {
			err = sess.WaitText(req.Pattern, timeout)
		}
	case req.Stable != "":
		quiet, parseErr := time.ParseDuration(req.Stable)
		if parseErr != nil {
			return Response{OK: false, Code: CodeFailure, Error: fmt.Sprintf("daemon: bad stable duration %q: %v", req.Stable, parseErr)}
		}
		err = sess.WaitStable(quiet, timeout)
	default:
		return Response{OK: false, Code: CodeFailure, Error: "daemon: wait needs --text or --stable"}
	}
	if err != nil {
		resp := failure(err)
		resp.Stage = "wait"
		if errors.Is(err, session.ErrStageTimeout) {
			resp.Code = CodeTimeout
		}
		return resp
	}
	return Response{OK: true, Name: req.Name}
}

func (s *Server) resize(req Request, sess *session.Session) Response {
	if err := sess.Resize(pty.Size{Cols: req.Cols, Rows: req.Rows}); err != nil {
		return failure(err)
	}
	size := sess.Size()
	return Response{OK: true, Name: req.Name, Cols: size.Cols, Rows: size.Rows}
}

// waitExit waits for the program to end on its own and reports its own exit code.
//
// It exists because "it has finished printing" and "it has exited" are different
// moments: a caller that asks for the exit code too early gets the code of a process
// this tool then has to kill — a CI-only flake in the daemon test is what made the
// distinction concrete.
func (s *Server) waitExit(req Request, sess *session.Session) Response {
	timeout := 10 * time.Second
	if req.Timeout != "" {
		parsed, err := time.ParseDuration(req.Timeout)
		if err != nil {
			return Response{OK: false, Code: CodeFailure, Error: fmt.Sprintf("daemon: bad timeout %q: %v", req.Timeout, err)}
		}
		timeout = parsed
	}

	done := make(chan struct{})
	var code int
	go func() {
		code, _ = sess.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		return Response{OK: false, Code: CodeTimeout, Stage: "wait-exit",
			Error: fmt.Sprintf("daemon: %s did not exit within %s", req.Name, timeout)}
	}

	s.mu.Lock()
	delete(s.sessions, req.Name)
	s.mu.Unlock()
	return Response{OK: true, Name: req.Name, Exited: true, HasExited: true, ExitCode: code}
}

func (s *Server) close(req Request, sess *session.Session) Response {
	code, err := sess.Close()
	if err != nil {
		return failure(err)
	}
	s.mu.Lock()
	delete(s.sessions, req.Name)
	s.mu.Unlock()
	return Response{OK: true, Name: req.Name, Exited: true, HasExited: true, ExitCode: code}
}

// screenResponse builds the common part of a screen answer and lets the caller
// fill in the one field it asked for.
func screenResponse(name string, sess *session.Session, set func(*Response)) Response {
	size := sess.Size()
	resp := Response{OK: true, Name: name, Cols: size.Cols, Rows: size.Rows}
	if sess.Exited() {
		resp.Exited, resp.HasExited = true, true
		code, _ := sess.Wait()
		resp.ExitCode = code
	}
	if set != nil {
		set(&resp)
	}
	return resp
}

func (s *Server) lookup(name string) (*session.Session, error) {
	if name == "" {
		return nil, fmt.Errorf("daemon: the command needs a session name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.sessions[name]
	if !ok {
		return nil, noSuchSession{name: name}
	}
	return e.sess, nil
}

// noSuchSession is reported as CodeNoSuch so a caller can tell "you typed the
// wrong name" from "the daemon refused".
type noSuchSession struct{ name string }

func (e noSuchSession) Error() string { return fmt.Sprintf("daemon: no session named %q", e.name) }

func failure(err error) Response {
	code := CodeFailure
	var missing noSuchSession
	if errors.As(err, &missing) {
		code = CodeNoSuch
	}
	return Response{OK: false, Code: code, Error: err.Error()}
}

// ── Client ──

// Client talks to a daemon, starting one if necessary.
type Client struct {
	Socket string
	// StartDaemon is how a missing daemon is launched; tests replace it with a
	// no-op and run a server in-process.
	StartDaemon func(socket string) error
	// Started reports whether this client had to launch the daemon. Only the
	// client that started one may release it: another client's sessions are not
	// ours to end.
	Started bool
}

// NewClient returns a client for the default socket.
func NewClient() *Client { return &Client{Socket: DefaultSocket()} }

// Release ends the daemon this client started, if it holds nothing any more.
//
// It is the polite exit: a command that had to start a daemon — a scenario run,
// or a one-off that found no session — would otherwise leave that process behind
// for the whole TTL. It never starts a daemon of its own: if the socket is
// already gone there is nothing to release.
func (c *Client) Release() {
	if !c.Started {
		return
	}
	_, _ = c.try(Request{Cmd: CmdRelease})
}

// Call sends one request, starting the daemon on the first attempt if needed.
func (c *Client) Call(req Request) (Response, error) {
	resp, err := c.try(req)
	if err == nil {
		return resp, nil
	}
	start := c.StartDaemon
	if start == nil {
		start = StartDetached
	}
	if startErr := start(c.Socket); startErr != nil {
		return Response{}, fmt.Errorf("daemon: start: %w", startErr)
	}
	c.Started = true
	// The daemon needs a moment to bind; retry for a second before giving up.
	var lastErr error
	for i := 0; i < 50; i++ {
		time.Sleep(20 * time.Millisecond)
		resp, lastErr = c.try(req)
		if lastErr == nil {
			return resp, nil
		}
	}
	return Response{}, fmt.Errorf("daemon: no daemon on %s: %w", c.Socket, lastErr)
}

func (c *Client) try(req Request) (Response, error) {
	conn, err := net.DialTimeout("unix", c.Socket, time.Second)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// StartDetached launches this executable as a daemon.
//
// It is detached from the caller (its own session) so that the command that
// started it can exit immediately: that is the whole point of the daemon.
func StartDetached(socket string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := socket + ".log"
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	cmd := exec.Command(exe, "daemon", "--socket", socket)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Env = append(os.Environ(), "TUIPROBE_SOCKET="+socket)
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// EnvSocket is the environment variable that points a command at a socket.
const EnvSocket = "TUIPROBE_SOCKET"

// SocketFromEnvOrFlag resolves the socket path: the flag wins, then the
// environment, then the default.
func SocketFromEnvOrFlag(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv(EnvSocket); env != "" {
		return env
	}
	return DefaultSocket()
}
