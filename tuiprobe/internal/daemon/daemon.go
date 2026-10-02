// Package daemon keeps tuiprobe sessions alive between commands.
//
// An agent, or a person, drives a TUI one command at a time: open it, look at the
// screen, press a key, look again. Each of those is a separate process, so the
// session has to live somewhere else — here, in a small daemon reached over a
// unix socket. The first command starts it; it exits on its own once it has been
// idle for a while, so nothing lingers.
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
	"strings"
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
	// SessionCnt has no omitempty: an empty daemon is a count of zero, not a
	// missing field, and a caller should not have to guess which it is.
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

// DefaultTTL is how long the daemon stays alive with no sessions and no traffic.
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

// Serve accepts connections until the daemon goes idle or Stop is called.
//
// It returns nil on an idle exit and the listener's error otherwise; the idle
// eviction is what keeps a session-scoped tool from leaving a process behind on
// a machine nobody is looking at.
func (s *Server) Serve(ln net.Listener) error {
	go s.reapIdle(ln)
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

// reapIdle stops the daemon once it has been unused for the whole TTL.
func (s *Server) reapIdle(ln net.Listener) {
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
		idle := len(s.sessions) == 0 && time.Since(s.lastSeen) > s.ttl
		s.mu.Unlock()
		if idle {
			s.Stop()
			_ = ln.Close()
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

	var req Request
	if err := dec.Decode(&req); err != nil {
		_ = enc.Encode(Response{OK: false, Code: CodeFailure, Error: fmt.Sprintf("daemon: read request: %v", err)})
		return
	}

	s.mu.Lock()
	s.lastSeen = time.Now()
	s.mu.Unlock()

	resp := s.dispatch(req)
	if err := enc.Encode(resp); err != nil {
		s.log("daemon: write response: %v", err)
	}
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
		go s.Stop()
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
	case "resize":
		return s.resize(req, sess)
	case "close":
		return s.close(req, sess)
	default:
		return Response{OK: false, Code: CodeFailure, Error: fmt.Sprintf("daemon: unknown command %q", req.Cmd)}
	}
}

func (s *Server) list() Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	resp := Response{OK: true, SessionCnt: len(s.sessions)}
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
	case req.Pattern != "":
		err = sess.WaitText(req.Pattern, timeout)
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
		if strings.Contains(err.Error(), "timed out") || strings.Contains(err.Error(), "still changing") {
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
}

// NewClient returns a client for the default socket.
func NewClient() *Client { return &Client{Socket: DefaultSocket()} }

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
