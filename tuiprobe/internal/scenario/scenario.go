// Package scenario runs a scripted terminal session: the reproducible form of
// "open it, do this, look at that".
//
// A scenario is a text file, one step per line, so it can be reviewed in a diff,
// written by hand and read by an agent:
//
//	# a welcome screen
//	open --size 80x24 -- ./myapp
//	wait --text "ready" --timeout 5s
//	golden testdata/golden/welcome_80x24.txt
//	fit --size 80x24
//	send --text ":help" --key enter
//	wait --text "Help"
//	golden --ansi testdata/golden/help_80x24.ansi
//	close
//	expect-exit 0
//
// Every failure names the line it happened on and the step as written, which is
// the difference between a red build and a diagnosis.
package scenario

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yusiwen/TinyCode/tuiprobe/golden"
	"github.com/yusiwen/TinyCode/tuiprobe/internal/daemon"
	"github.com/yusiwen/TinyCode/tuiprobe/internal/shot"
	"github.com/yusiwen/TinyCode/tuiprobe/session"
)

// DefaultTimeout bounds a step that does not give its own.
var DefaultTimeout = 10 * time.Second

// verbs are the step verbs this package accepts, in the order the references
// present them; an alias (`diff` for `golden`, `shot` for `screenshot`) is listed
// with the verb it names, because both spellings are accepted by step().
//
// It is the single authority for the three surfaces that enumerate the vocabulary:
// this switch, docs/scenario.md and the CLI help. They drifted twice — the help
// listed ten of thirteen verbs until issue #145, and the reference table had missed
// `screenshot` until issue #142 — because each one was written by hand.
var verbs = []string{
	"open", "send", "wait", "mark", "stable", "sleep",
	"golden", "diff", "fit", "screenshot", "shot",
	"close", "wait-exit", "expect-exit",
}

// Verbs returns the step verbs the runner accepts, as a copy: a surface that lists
// them cannot change what the runner accepts.
func Verbs() []string { return append([]string(nil), verbs...) }

// Step is one line of a scenario.
type Step struct {
	Line int
	Verb string
	Args []string
	Text string // the line as written, for error messages
}

// Options configure a run.
type Options struct {
	Socket string
	// Dir is the working directory for golden paths and for the program.
	Dir string
	// Update rewrites goldens instead of comparing them.
	Update bool
	// Gate is an environment variable that must be set for the scenario to run;
	// when it is unset the run is skipped with that reason instead of passing
	// silently.
	Gate string
	// Name is the session name the scenario uses.
	Name string
	// Stdout receives the run's progress; nil discards it.
	Stdout io.Writer
}

// Result is what a run produced.
type Result struct {
	Skipped bool
	Reason  string
	Steps   int
}

// Parse reads a scenario.
func Parse(r io.Reader) ([]Step, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var steps []Step
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields, err := splitFields(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		steps = append(steps, Step{Line: n, Verb: fields[0], Args: fields[1:], Text: line})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, errors.New("scenario: no steps")
	}
	return steps, nil
}

// splitFields splits a line into fields, honouring double and single quotes.
//
// Quoting matters because a step's argument is often a whole command line: without
// it, `open -- /bin/sh -c 'echo ready; read l'` reaches the shell as a syntax
// error, and the scenario fails with a screen nobody expects.
func splitFields(line string) ([]string, error) {
	var fields []string
	var current strings.Builder
	var quote byte
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
				continue
			}
			current.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
		case c == ' ' || c == '\t':
			if current.Len() > 0 {
				fields = append(fields, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(c)
		}
	}
	if quote != 0 {
		return nil, errors.New("unbalanced quote")
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}
	if len(fields) == 0 {
		return nil, errors.New("empty step")
	}
	return fields, nil
}

// Run executes a scenario against a daemon.
func Run(steps []Step, opts Options) (Result, error) {
	if opts.Name == "" {
		opts.Name = "scenario"
	}
	if opts.Gate != "" && os.Getenv(opts.Gate) == "" {
		return Result{Skipped: true, Reason: opts.Gate + " is not set"}, nil
	}

	logf := func(format string, args ...any) {
		if opts.Stdout != nil {
			fmt.Fprintf(opts.Stdout, format+"\n", args...)
		}
	}

	client := &daemon.Client{Socket: daemon.SocketFromEnvOrFlag(opts.Socket)}
	runner := &runner{opts: opts, client: client, logf: logf}
	// Cleanup closes the session first: only then has the daemon nothing left to
	// hold, and only a daemon this run started is released — a scenario driven
	// against someone else's long-lived daemon must not end it.
	defer func() {
		runner.cleanup()
		client.Release()
	}()

	for _, step := range steps {
		if err := runner.step(step); err != nil {
			return Result{Steps: runner.count}, fmt.Errorf("line %d: %s: %w", step.Line, step.Text, err)
		}
		runner.count++
	}
	return Result{Steps: runner.count}, nil
}

type runner struct {
	opts   Options
	client *daemon.Client
	logf   func(string, ...any)
	count  int

	opened   bool
	exitCode int
	hasExit  bool
}

func (r *runner) call(req daemon.Request) (daemon.Response, error) {
	if req.Name == "" {
		req.Name = r.opts.Name
	}
	resp, err := r.client.Call(req)
	if err != nil {
		return resp, err
	}
	if !resp.OK {
		// Keep the class the daemon put on its refusal: a wait that ran out of time is a
		// timeout (exit code 3), not a generic failure, and flattening every refusal into
		// one message made `tuiprobe run` answer 2 for exactly the case the contract
		// promises 3 (issue #153).
		if resp.Code == daemon.CodeTimeout {
			return resp, timeoutError{msg: resp.Error}
		}
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

// timeoutError is a daemon refusal that was a timeout, carrying the daemon's own message
// and the class callers already classify with: the CLI's command path answers 3 by
// reading the response code, and the run path now answers 3 because errors.Is finds
// session.ErrStageTimeout here (issue #153).
type timeoutError struct{ msg string }

func (e timeoutError) Error() string { return e.msg }

func (e timeoutError) Is(target error) bool { return target == session.ErrStageTimeout }

// cleanup closes the session if the scenario did not, so a failing scenario does
// not leave a program behind.
func (r *runner) cleanup() {
	if r.opened {
		_, _ = r.call(daemon.Request{Cmd: "close"})
	}
}

func (r *runner) step(s Step) error {
	switch s.Verb {
	case "open":
		return r.stepOpen(s)
	case "send":
		return r.stepSend(s)
	case "wait":
		return r.stepWait(s)
	case "mark":
		return r.stepMark(s)
	case "stable":
		return r.stepStable(s)
	case "sleep":
		d, err := firstDuration(s.Args, "sleep")
		if err != nil {
			return err
		}
		time.Sleep(d)
		return nil
	case "golden", "diff":
		return r.stepGolden(s)
	case "fit":
		return r.stepFit(s)
	case "screenshot", "shot":
		return r.stepScreenshot(s)
	case "wait-exit":
		return r.stepWaitExit(s)
	case "close":
		return r.stepClose(s, false)
	case "expect-exit":
		return r.stepClose(s, true)
	default:
		return fmt.Errorf("unknown step %q", s.Verb)
	}
}

func (r *runner) stepOpen(s Step) error {
	values, lists, rest, err := parseStep(s, stepFlags{
		values: map[string]bool{"--size": true, "--dir": true},
		lists:  map[string]bool{"--env": true},
	})
	if err != nil {
		return err
	}
	if len(rest) == 0 {
		return errors.New("open needs a command: open -- ./myapp")
	}
	cols, rows := 80, 24
	if size := values["--size"]; size != "" {
		if cols, rows, err = parseSize(size); err != nil {
			return err
		}
	}
	dir := values["--dir"]
	if dir == "" {
		dir = r.opts.Dir
	}
	resp, err := r.call(daemon.Request{
		Cmd: "open", Args: rest, Dir: dir, Env: lists["--env"],
		Cols: cols, Rows: rows,
	})
	if err != nil {
		return err
	}
	r.opened = true
	r.logf("opened %s pid=%d %dx%d", resp.Name, resp.Pid, resp.Cols, resp.Rows)
	return nil
}

func (r *runner) stepSend(s Step) error {
	values, lists, _, err := parseStep(s, stepFlags{
		values: map[string]bool{"--text": true},
		lists:  map[string]bool{"--key": true},
	})
	if err != nil {
		return err
	}
	text := values["--text"]
	keys := lists["--key"]
	if text == "" && len(keys) == 0 {
		return errors.New("send needs --text, --key, or both")
	}
	_, err = r.call(daemon.Request{Cmd: "send", Text: text, Keys: keys})
	return err
}

func (r *runner) stepWait(s Step) error {
	pattern := flagValue(s, "--text")
	if pattern == "" {
		return errors.New("wait needs --text <regexp>")
	}
	timeout, err := durationFlag(s, "--timeout", DefaultTimeout)
	if err != nil {
		return err
	}
	since := hasFlag(s, "--since")
	if _, err := r.call(daemon.Request{Cmd: "wait", Pattern: pattern, Timeout: timeout.String(), Since: since}); err != nil {
		return err
	}
	if since {
		r.logf("saw %s (since the mark)", pattern)
		return nil
	}
	r.logf("saw %s", pattern)
	return nil
}

// stepMark records how far the program's output has come, so a later `wait --since`
// only accepts text drawn after this line.
//
// It exists for the flow a single screen cannot express: a scenario that starts the
// same program twice gets the same frame twice, and a wait placed after the second
// start was satisfied by the first run's leftovers. The workaround was a unique
// marker printed between the runs, which proves the wrapper reached a line, not that
// the new program is on screen (issue #126).
func (r *runner) stepMark(s Step) error {
	if !r.opened {
		return errors.New("mark needs an open first")
	}
	if _, _, _, err := parseStep(s, stepFlags{}); err != nil {
		return err
	}
	resp, err := r.call(daemon.Request{Cmd: "mark"})
	if err != nil {
		return err
	}
	r.logf("marked %s (generation %d)", resp.Name, resp.Generation)
	return nil
}

func (r *runner) stepStable(s Step) error {
	quiet, err := firstDuration(s.Args, "stable")
	if err != nil {
		return err
	}
	timeout, err := durationFlag(s, "--timeout", DefaultTimeout)
	if err != nil {
		return err
	}
	_, err = r.call(daemon.Request{Cmd: "wait", Stable: quiet.String(), Timeout: timeout.String()})
	return err
}

func (r *runner) stepGolden(s Step) error {
	values, _, rest, err := parseStep(s, stepFlags{
		values: map[string]bool{"--against": true},
		bools:  map[string]bool{"--ansi": true},
	})
	if err != nil {
		return err
	}
	path := values["--against"]
	if path == "" {
		if len(rest) != 1 || strings.HasPrefix(rest[0], "--") {
			return fmt.Errorf("%s needs one file: %s <file>", s.Verb, s.Verb)
		}
		path = rest[0]
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.opts.Dir, path)
	}

	ansi := hasFlag(s, "--ansi")
	verb, artifact := "text", ""
	resp, err := r.call(daemon.Request{Cmd: verb})
	if err != nil {
		return err
	}
	artifact = resp.Text
	if ansi {
		if resp, err = r.call(daemon.Request{Cmd: "ansi"}); err != nil {
			return err
		}
		artifact = resp.ANSI
	} else {
		artifact = golden.Normalize(artifact)
	}

	if r.opts.Update {
		if err := golden.Write(filepath.Dir(path), filepath.Base(path), artifact); err != nil {
			return err
		}
		r.logf("updated %s", path)
		return nil
	}
	if err := golden.Compare(filepath.Dir(path), filepath.Base(path), artifact); err != nil {
		return err
	}
	r.logf("matches %s", path)
	return nil
}

// stepFit asserts the geometry a screen claims: the session must actually be that
// size, and the screen must fit inside it.
//
// The second half is nearly free — the emulator wraps at its own width — but the
// first half is not: it is what catches a scenario whose committed goldens were
// captured at a different terminal than the one it now runs with.
func (r *runner) stepFit(s Step) error {
	resp, err := r.call(daemon.Request{Cmd: "text"})
	if err != nil {
		return err
	}
	cols, rows := resp.Cols, resp.Rows
	if size := flagValue(s, "--size"); size != "" {
		claimCols, claimRows, err := parseSize(size)
		if err != nil {
			return err
		}
		if claimCols != resp.Cols || claimRows != resp.Rows {
			return fmt.Errorf("the session is %dx%d, but the scenario claims %dx%d", resp.Cols, resp.Rows, claimCols, claimRows)
		}
		cols, rows = claimCols, claimRows
	}
	return golden.Fits(cols, rows, resp.Text)
}

// stepScreenshot renders the current screen to a file, asserting that the image's
// size follows from the geometry.
func (r *runner) stepScreenshot(s Step) error {
	values, _, rest, err := parseStep(s, stepFlags{
		values: map[string]bool{"--scale": true, "--font": true, "--format": true, "--renderer": true, "--browser": true},
	})
	if err != nil {
		return err
	}
	if len(rest) != 1 || strings.HasPrefix(rest[0], "--") {
		return fmt.Errorf("%s needs one file: %s <file>", s.Verb, s.Verb)
	}
	path := rest[0]
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.opts.Dir, path)
	}

	resp, err := r.call(daemon.Request{Cmd: "ansi"})
	if err != nil {
		return err
	}
	scale := 1
	if raw := values["--scale"]; raw != "" {
		if scale, err = strconv.Atoi(raw); err != nil || scale < 1 {
			return fmt.Errorf("--scale %q must be a positive integer", raw)
		}
	}
	artifact, err := shot.Render(resp.ANSI, resp.Cols, resp.Rows, shot.Options{
		Format: values["--format"], Scale: scale, FontPath: values["--font"],
		Renderer: values["--renderer"], Browser: values["--browser"],
	})
	if err != nil {
		return err
	}
	if err := shot.Write(path, artifact); err != nil {
		return err
	}
	// Read the file back: the assertion is about the artifact, not about what the
	// renderer believed it wrote.
	if artifact.Format == shot.FormatPNG {
		if err := shot.VerifyPNG(path, resp.Cols, resp.Rows, shot.Options{Scale: scale, Renderer: artifact.Renderer}); err != nil {
			return err
		}
	}
	r.logf("wrote %s %dx%d", path, artifact.Width, artifact.Height)
	return nil
}

// stepWaitExit waits for the program to end on its own and remembers its code.
//
// "It stopped printing" and "it exited" are different moments: `expect-exit` right
// after a `send` closes — and therefore kills — a program that was about to leave by
// itself, which reports -1 for something that exits 0. The first scenario file written
// against this repository hit exactly that, so the vocabulary gained the step the CLI
// and the daemon already had.
func (r *runner) stepWaitExit(s Step) error {
	if !r.opened {
		return errors.New("wait-exit needs an open first")
	}
	timeout := DefaultTimeout
	if len(s.Args) > 0 {
		parsed, err := time.ParseDuration(s.Args[0])
		if err != nil {
			return fmt.Errorf("wait-exit takes a duration, got %q", s.Args[0])
		}
		timeout = parsed
	}
	resp, err := r.call(daemon.Request{Cmd: "wait-exit", Timeout: timeout.String()})
	if err != nil {
		return err
	}
	r.opened, r.exitCode, r.hasExit = false, resp.ExitCode, true
	r.logf("exited %s (exit %d)", resp.Name, resp.ExitCode)
	return nil
}

func (r *runner) stepClose(s Step, expect bool) error {
	want := 0
	if expect {
		if len(s.Args) == 0 {
			return errors.New("expect-exit needs a number")
		}
		parsed, err := strconv.Atoi(s.Args[0])
		if err != nil {
			return fmt.Errorf("expect-exit needs a number, got %q", s.Args[0])
		}
		want = parsed
	}
	if r.opened {
		resp, err := r.call(daemon.Request{Cmd: "close"})
		if err != nil {
			return err
		}
		r.opened, r.exitCode, r.hasExit = false, resp.ExitCode, true
		r.logf("closed %s (exit %d)", resp.Name, resp.ExitCode)
	}
	if !expect {
		return nil
	}
	if !r.hasExit {
		return errors.New("expect-exit needs a close first")
	}
	if r.exitCode != want {
		return fmt.Errorf("program exited with %d, expected %d", r.exitCode, want)
	}
	return nil
}

// stepFlags declares which flags a step accepts.
type stepFlags struct {
	values map[string]bool // one value: --size 80x24 or --size=80x24
	lists  map[string]bool // repeatable: --key enter --key ctrl+c
	bools  map[string]bool // valueless: --ansi
}

// parseStep walks a step's arguments, returning the flag values, the repeatable
// values and everything that is not a flag. A scenario is a script, not a
// language, so this stays tiny on purpose.
func parseStep(s Step, spec stepFlags) (map[string]string, map[string][]string, []string, error) {
	values := map[string]string{}
	lists := map[string][]string{}
	var rest []string

	for i := 0; i < len(s.Args); i++ {
		arg := s.Args[i]
		if arg == "--" {
			// Everything after the separator belongs to the program, even if it
			// looks like one of our flags.
			rest = append(rest, s.Args[i+1:]...)
			return values, lists, rest, nil
		}
		name, inline, hasInline := strings.Cut(arg, "=")
		switch {
		case spec.bools[name]:
			continue
		case spec.values[name] || spec.lists[name]:
			value := inline
			if !hasInline {
				if i+1 >= len(s.Args) {
					return nil, nil, nil, fmt.Errorf("%s needs a value", name)
				}
				i++
				value = s.Args[i]
			}
			if spec.values[name] {
				values[name] = value
			} else {
				lists[name] = append(lists[name], value)
			}
		default:
			rest = append(rest, arg)
		}
	}
	return values, lists, rest, nil
}

// flagValue reads --flag <value> or --flag=<value> from a step.
func flagValue(s Step, flag string) string {
	for i, a := range s.Args {
		if a == flag && i+1 < len(s.Args) {
			return s.Args[i+1]
		}
		if strings.HasPrefix(a, flag+"=") {
			return strings.SplitN(a, "=", 2)[1]
		}
	}
	return ""
}

func hasFlag(s Step, flag string) bool {
	for _, a := range s.Args {
		if a == flag {
			return true
		}
	}
	return false
}

// durationFlag reads a --flag duration with a default.
func durationFlag(s Step, flag string, fallback time.Duration) (time.Duration, error) {
	raw := flagValue(s, flag)
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", flag, err)
	}
	return d, nil
}

// firstDuration finds a bare duration argument, used by sleep and stable.
func firstDuration(args []string, verb string) (time.Duration, error) {
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			continue
		}
		if d, err := time.ParseDuration(a); err == nil {
			return d, nil
		}
	}
	return 0, fmt.Errorf("%s needs a duration, for example 200ms", verb)
}

func parseSize(s string) (int, int, error) {
	parts := strings.SplitN(strings.ToLower(s), "x", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("size %q is not WxH", s)
	}
	cols, err := strconv.Atoi(parts[0])
	if err != nil || cols <= 0 {
		return 0, 0, fmt.Errorf("size %q has a bad width", s)
	}
	rows, err := strconv.Atoi(parts[1])
	if err != nil || rows <= 0 {
		return 0, 0, fmt.Errorf("size %q has a bad height", s)
	}
	return cols, rows, nil
}
