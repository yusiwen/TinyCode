package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/yusiwen/TinyCode/tuiprobe/internal/daemon"
)

// stringList collects a repeatable flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	*l = append(*l, v)
	return nil
}

// sessionCommand is the shared shape of the session commands: the flags every one
// of them has, plus the parsed leftovers.
type sessionCommand struct {
	fs     *flag.FlagSet
	socket string
	name   string
	asJSON bool
	stdout io.Writer
	stderr io.Writer
}

// newSessionCommand registers the shared flags, lets the command add its own, and
// parses once.
func newSessionCommand(verb string, args []string, stdout, stderr io.Writer, register func(*flag.FlagSet)) (*sessionCommand, error) {
	cmd := &sessionCommand{fs: flag.NewFlagSet(verb, flag.ContinueOnError), stdout: stdout, stderr: stderr}
	cmd.fs.SetOutput(stderr)
	cmd.fs.StringVar(&cmd.socket, "socket", "", "daemon socket (default: $TUIPROBE_SOCKET, else a per-user path in $TMPDIR)")
	cmd.fs.StringVar(&cmd.name, "name", "default", "session name")
	cmd.fs.BoolVar(&cmd.asJSON, "json", false, "print the answer as JSON")
	if register != nil {
		register(cmd.fs)
	}
	if err := cmd.fs.Parse(args); err != nil {
		return nil, err
	}
	return cmd, nil
}

// call sends a request to the daemon and maps the answer onto an exit code. The
// session name defaults to the command's --name, so a caller cannot forget it and
// get a confusing "needs a session name" back.
func (c *sessionCommand) call(req daemon.Request) (daemon.Response, int) {
	if req.Name == "" {
		req.Name = c.name
	}
	client := &daemon.Client{Socket: daemon.SocketFromEnvOrFlag(c.socket)}
	// A command that had to start a daemon hands it back once the daemon holds
	// nothing: `open` keeps it — a session is there — while a lookup that found
	// no session, or a one-off against a stale socket, does not leave a process
	// behind. A daemon another client started is never touched: Started is only
	// set for one this command launched.
	defer client.Release()
	resp, err := client.Call(req)
	if err != nil {
		fmt.Fprintf(c.stderr, "tuiprobe: %v\n", err)
		return daemon.Response{}, daemon.CodeFailure
	}
	if !resp.OK {
		if c.asJSON {
			printJSON(c.stdout, resp)
		} else {
			fmt.Fprintf(c.stderr, "tuiprobe: %s\n", resp.Error)
		}
		if resp.Code == 0 {
			return resp, daemon.CodeFailure
		}
		return resp, resp.Code
	}
	return resp, daemon.CodeOK
}

func printJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// fail prints a usage error and returns the failure code.
func fail(stderr io.Writer, verb, format string, args ...any) int {
	fmt.Fprintf(stderr, "tuiprobe "+verb+": "+format+"\n", args...)
	return daemon.CodeFailure
}

// runOpen starts a program in a new session.
func runOpen(args []string, stdout, stderr io.Writer) int {
	var size, dir string
	var env stringList
	cmd, err := newSessionCommand("open", args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&size, "size", "80x24", "terminal size, WxH")
		fs.StringVar(&dir, "dir", "", "working directory")
		fs.Var(&env, "env", "extra environment variable KEY=value (repeatable)")
	})
	if err != nil {
		return daemon.CodeFailure
	}
	argv := cmd.fs.Args()
	if len(argv) == 0 {
		return fail(stderr, "open", "a command is required, for example: tuiprobe open -- ./myapp")
	}
	cols, rows, err := parseSize(size)
	if err != nil {
		return fail(stderr, "open", "%v", err)
	}

	resp, code := cmd.call(daemon.Request{Cmd: "open", Name: cmd.name, Args: argv, Dir: dir, Env: env, Cols: cols, Rows: rows})
	if code != daemon.CodeOK {
		return code
	}
	if cmd.asJSON {
		printJSON(stdout, resp)
		return daemon.CodeOK
	}
	fmt.Fprintf(stdout, "%s pid=%d %dx%d\n", resp.Name, resp.Pid, resp.Cols, resp.Rows)
	return daemon.CodeOK
}

// runSend presses keys and/or types text.
func runSend(args []string, stdout, stderr io.Writer) int {
	var text string
	var keys stringList
	var repeat int
	cmd, err := newSessionCommand("send", args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&text, "text", "", "literal text to send")
		fs.Var(&keys, "key", "key name to send: enter, tab, ctrl+c, alt+left, f5, … (repeatable)")
		fs.IntVar(&repeat, "repeat", 1, "send every --key this many times")
	})
	if err != nil {
		return daemon.CodeFailure
	}
	if text == "" && len(keys) == 0 {
		return fail(stderr, "send", "give --text, --key, or both")
	}
	if repeat < 1 {
		return fail(stderr, "send", "--repeat must be at least 1")
	}

	expanded := make([]string, 0, len(keys)*repeat)
	for _, key := range keys {
		for i := 0; i < repeat; i++ {
			expanded = append(expanded, key)
		}
	}

	resp, code := cmd.call(daemon.Request{Cmd: "send", Name: cmd.name, Text: text, Keys: expanded})
	if code != daemon.CodeOK {
		return code
	}
	if cmd.asJSON {
		printJSON(stdout, resp)
	} else {
		fmt.Fprintln(stdout, "sent")
	}
	return daemon.CodeOK
}

// runWait waits for the screen to match a pattern or to settle.
func runWait(args []string, stdout, stderr io.Writer) int {
	var pattern, stable string
	var timeout time.Duration
	var since bool
	cmd, err := newSessionCommand("wait", args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&pattern, "text", "", "wait until the screen matches this regular expression")
		fs.StringVar(&stable, "stable", "", "wait until the screen has not changed for this long, for example 200ms")
		fs.DurationVar(&timeout, "timeout", 10*time.Second, "give up after this long")
		fs.BoolVar(&since, "since", false, "match only text the program drew after the last mark")
	})
	if err != nil {
		return daemon.CodeFailure
	}
	if pattern == "" && stable == "" {
		return fail(stderr, "wait", "give --text or --stable")
	}
	if since && pattern == "" {
		return fail(stderr, "wait", "--since narrows --text, so it needs one")
	}

	resp, code := cmd.call(daemon.Request{Cmd: "wait", Name: cmd.name, Pattern: pattern, Stable: stable, Timeout: timeout.String(), Since: since})
	if code != daemon.CodeOK {
		return code
	}
	if cmd.asJSON {
		printJSON(stdout, resp)
	} else {
		fmt.Fprintln(stdout, "ready")
	}
	return daemon.CodeOK
}

// runMark records how far the program's output has reached, so a later `wait --since`
// can tell text drawn from then on from text that was already on the screen.
//
// It exists for the flow a single screen cannot express: a program started twice in one
// session paints the same frame twice, so the second run's prompt is indistinguishable
// from the first run's leftovers (issue #126).
func runMark(args []string, stdout, stderr io.Writer) int {
	cmd, err := newSessionCommand("mark", args, stdout, stderr, nil)
	if err != nil {
		return daemon.CodeFailure
	}

	resp, code := cmd.call(daemon.Request{Cmd: "mark", Name: cmd.name})
	if code != daemon.CodeOK {
		return code
	}
	if cmd.asJSON {
		printJSON(stdout, resp)
		return daemon.CodeOK
	}
	fmt.Fprintf(stdout, "marked %s at generation %d\n", resp.Name, resp.Generation)
	return daemon.CodeOK
}

// runScreen prints the screen in one of its forms (text, ansi, html, trace).
func runScreen(verb string, args []string, stdout, stderr io.Writer) int {
	var n int
	cmd, err := newSessionCommand(verb, args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.IntVar(&n, "n", 4096, "for trace: how many bytes of the raw stream to show")
	})
	if err != nil {
		return daemon.CodeFailure
	}

	resp, code := cmd.call(daemon.Request{Cmd: verb, Name: cmd.name, N: n})
	if code != daemon.CodeOK {
		return code
	}
	if cmd.asJSON {
		printJSON(stdout, resp)
		return daemon.CodeOK
	}
	switch verb {
	case "text":
		// Trim the blank rows below the content for a human or an agent reading
		// the output: the geometry is in --json for anything that needs it.
		fmt.Fprint(stdout, strings.TrimRight(resp.Text, "\n")+"\n")
	case "ansi":
		fmt.Fprint(stdout, resp.ANSI)
	case "html":
		fmt.Fprint(stdout, resp.HTML)
	case "trace":
		fmt.Fprint(stdout, resp.Trace)
	}
	return daemon.CodeOK
}

// runResize changes the terminal geometry.
func runResize(args []string, stdout, stderr io.Writer) int {
	var size string
	cmd, err := newSessionCommand("resize", args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&size, "size", "80x24", "new terminal size, WxH")
	})
	if err != nil {
		return daemon.CodeFailure
	}
	cols, rows, err := parseSize(size)
	if err != nil {
		return fail(stderr, "resize", "%v", err)
	}
	resp, code := cmd.call(daemon.Request{Cmd: "resize", Name: cmd.name, Cols: cols, Rows: rows})
	if code != daemon.CodeOK {
		return code
	}
	if cmd.asJSON {
		printJSON(stdout, resp)
	} else {
		fmt.Fprintf(stdout, "%s %dx%d\n", resp.Name, resp.Cols, resp.Rows)
	}
	return daemon.CodeOK
}

// runClose ends a session and reports how the program exited.
func runClose(args []string, stdout, stderr io.Writer) int {
	expect := -1
	cmd, err := newSessionCommand("close", args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.IntVar(&expect, "expect-exit", -1, "fail unless the program exited with this code")
	})
	if err != nil {
		return daemon.CodeFailure
	}

	resp, code := cmd.call(daemon.Request{Cmd: "close", Name: cmd.name})
	if code != daemon.CodeOK {
		return code
	}
	if expect >= 0 && resp.ExitCode != expect {
		msg := fmt.Sprintf("%s exited with %d, expected %d", resp.Name, resp.ExitCode, expect)
		if cmd.asJSON {
			printJSON(stdout, daemon.Response{OK: false, Code: daemon.CodeFailure, Error: msg, Name: resp.Name, ExitCode: resp.ExitCode, HasExited: true})
		} else {
			fmt.Fprintf(stderr, "tuiprobe close: %s\n", msg)
		}
		return daemon.CodeFailure
	}
	if cmd.asJSON {
		printJSON(stdout, resp)
	} else {
		fmt.Fprintf(stdout, "%s exited with %d\n", resp.Name, resp.ExitCode)
	}
	return daemon.CodeOK
}

// runWaitExit waits for a program to end on its own and reports its code, which is
// the deterministic version of "close and look at the result".
func runWaitExit(args []string, stdout, stderr io.Writer) int {
	var timeout time.Duration
	cmd, err := newSessionCommand("wait-exit", args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.DurationVar(&timeout, "timeout", 10*time.Second, "give up after this long")
	})
	if err != nil {
		return daemon.CodeFailure
	}
	resp, code := cmd.call(daemon.Request{Cmd: "wait-exit", Timeout: timeout.String()})
	if code != daemon.CodeOK {
		return code
	}
	if cmd.asJSON {
		printJSON(stdout, resp)
	} else {
		fmt.Fprintf(stdout, "%s exited with %d\n", resp.Name, resp.ExitCode)
	}
	return daemon.CodeOK
}

// runSessions lists the live sessions.
func runSessions(args []string, stdout, stderr io.Writer) int {
	cmd, err := newSessionCommand("sessions", args, stdout, stderr, nil)
	if err != nil {
		return daemon.CodeFailure
	}
	resp, code := cmd.call(daemon.Request{Cmd: "sessions"})
	if code != daemon.CodeOK {
		return code
	}
	if cmd.asJSON {
		printJSON(stdout, resp)
		return daemon.CodeOK
	}
	if len(resp.Sessions) == 0 {
		fmt.Fprintln(stdout, "no sessions")
		return daemon.CodeOK
	}
	for _, s := range resp.Sessions {
		state := "running"
		if s.Exited {
			state = "exited"
		}
		fmt.Fprintf(stdout, "%s pid=%d %dx%d %s\n", s.Name, s.Pid, s.Cols, s.Rows, state)
	}
	return daemon.CodeOK
}

// runDaemon is the internal entry point the client launches to start one. It is a
// normal command so a single binary can be both the client and the server.
func runDaemon(args []string, stdout, stderr io.Writer) int {
	var socket string
	var ttl time.Duration
	fs := flag.NewFlagSet("daemon", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&socket, "socket", "", "socket to listen on")
	fs.DurationVar(&ttl, "ttl", daemon.DefaultTTL, "close the sessions and exit after this long with no command")
	if err := fs.Parse(args); err != nil {
		return daemon.CodeFailure
	}

	srv := daemon.New(daemon.SocketFromEnvOrFlag(socket), ttl)
	srv.SetLogger(func(format string, a ...any) { fmt.Fprintf(stderr, format+"\n", a...) })
	ln, err := srv.Listen()
	if err != nil {
		fmt.Fprintf(stderr, "tuiprobe daemon: %v\n", err)
		return daemon.CodeFailure
	}
	if err := srv.Serve(ln); err != nil {
		fmt.Fprintf(stderr, "tuiprobe daemon: %v\n", err)
		return daemon.CodeFailure
	}
	return daemon.CodeOK
}
