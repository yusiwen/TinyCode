// Package verify discovers a project's test command, runs it within bounds, and
// reports what happened (roadmap C5).
//
// It exists as its own package because `tool` imports `agent`: the loop needs to
// run the suite after a write, and the model needs a tool that runs it, and only a
// package below both can serve them.
package verify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Limits are the defaults a caller can tighten, not loosen: a test command is the
// project's own, and one that hangs must not hang the agent.
const (
	DefaultTimeout   = 120 * time.Second
	MaxTimeout       = 600 * time.Second
	DefaultMaxOutput = 4000
	// tailLines is how much of a failing run is kept before the byte cap applies:
	// the failure is usually the last thing printed.
	waitDelay = 500 * time.Millisecond
)

// Options bound one run.
type Options struct {
	// Timeout is the run's bound; zero means DefaultTimeout, and anything above
	// MaxTimeout is clamped.
	Timeout time.Duration
	// MaxOutput caps the tail kept in the report, in bytes.
	MaxOutput int
}

func (o Options) timeout() time.Duration {
	t := o.Timeout
	if t <= 0 {
		t = DefaultTimeout
	}
	if t > MaxTimeout {
		t = MaxTimeout
	}
	return t
}

func (o Options) maxOutput() int {
	if o.MaxOutput <= 0 {
		return DefaultMaxOutput
	}
	return o.MaxOutput
}

// Report is what a run produced.
type Report struct {
	// Command is what ran.
	Command string
	// Passed is exit status 0.
	Passed bool
	// TimedOut is true when the bound killed it: that is not a test failure and
	// the caller should say so.
	TimedOut bool
	// ExitCode is the process's status; -1 when it was killed or never started.
	ExitCode int
	// Duration is how long it took.
	Duration time.Duration
	// Output is the tail of stdout+stderr, bounded by MaxOutput.
	Output string
}

// Summary is the one line a reader and the model see first.
func (r Report) Summary() string {
	switch {
	case r.TimedOut:
		return fmt.Sprintf("tests timed out after %s (%s)", r.Duration.Round(time.Millisecond), r.Command)
	case r.Passed:
		return fmt.Sprintf("tests passed in %s (%s)", r.Duration.Round(time.Millisecond), r.Command)
	default:
		return fmt.Sprintf("tests failed (exit %d) in %s (%s)", r.ExitCode, r.Duration.Round(time.Millisecond), r.Command)
	}
}

// String renders the report for a tool result: the summary, then the bounded tail
// when there is something to read.
func (r Report) String() string {
	if r.Output == "" {
		return r.Summary()
	}
	return r.Summary() + "\n" + r.Output
}

// Config is the part of the test section this package needs.
type Config struct {
	// Command overrides discovery.
	Command string
	// TimeoutMS bounds the run.
	TimeoutMS int
	// MaxOutputBytes caps the tail.
	MaxOutputBytes int
}

// command builds the effective command: the configured one first, then the
// project's markers.
func Discover(dir string, cfg Config) (string, error) {
	if cmd := strings.TrimSpace(cfg.Command); cmd != "" {
		return cmd, nil
	}
	if fileExists(filepath.Join(dir, "go.mod")) {
		return "go test ./...", nil
	}
	if path := filepath.Join(dir, "package.json"); fileExists(path) {
		if hasNpmTest(path) {
			return "npm test --silent", nil
		}
	}
	if path := filepath.Join(dir, "Makefile"); fileExists(path) {
		if hasMakeTarget(path, "test:") {
			return "make test", nil
		}
	}
	return "", errors.New("no test command found: add one to the project (go.mod, package.json's test script, a Makefile test target) or set test.command in the config")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// hasNpmTest reports whether package.json declares a test script. A missing or
// unparseable file is "no": npm would run its own placeholder message and report
// success, which is worse than reporting that nothing runs.
func hasNpmTest(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	text := string(data)
	i := strings.Index(text, `"scripts"`)
	if i < 0 {
		return false
	}
	rest := text[i:]
	return strings.Contains(rest, `"test"`)
}

// hasMakeTarget reports whether a Makefile defines the target.
func hasMakeTarget(path, target string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), target) {
			return true
		}
	}
	return false
}

// Run executes command in dir with the given bounds.
//
// The command is the project's own, so it is run through `sh -c` and inherits the
// environment: this is not a sandbox, and the doc says so. What this function
// guarantees is the bound: a command that outlives its timeout is killed, and
// WaitDelay stops a child that inherited the output pipe from holding the call
// open past the kill (the defect C4's CI caught).
func Run(ctx context.Context, dir, command string, opts Options) (Report, error) {
	if strings.TrimSpace(command) == "" {
		return Report{}, errors.New("no test command to run")
	}
	runCtx, cancel := context.WithTimeout(ctx, opts.timeout())
	defer cancel()

	started := time.Now()
	cmd := exec.CommandContext(runCtx, "sh", "-c", command)
	cmd.Dir = dir
	cmd.WaitDelay = waitDelay
	out, err := cmd.CombinedOutput()

	report := Report{
		Command:  command,
		Duration: time.Since(started),
		ExitCode: exitCode(err),
		Output:   tail(string(out), opts.maxOutput()),
	}
	switch {
	case runCtx.Err() == context.DeadlineExceeded:
		report.TimedOut = true
		report.ExitCode = -1
	case err == nil:
		report.Passed = true
	default:
		// A non-zero exit is a failed suite: the output says which test failed.
	}
	return report, nil
}

// IsWriteTool reports whether a tool's success can change what the tests see.
// `bash` is deliberately absent: a shell command's effects are not a single file,
// and running the suite after every one would be unbounded.
func IsWriteTool(name string) bool {
	switch name {
	case "write_file", "edit", "apply_patch", "write":
		return true
	}
	return false
}

// exitCode is the process's status, or -1 when it never ran or was killed.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// tail keeps the last max bytes of output, on a line boundary when it has to cut.
func tail(s string, max int) string {
	s = strings.TrimRight(s, "\n")
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := s[len(s)-max:]
	if i := strings.Index(cut, "\n"); i >= 0 {
		cut = cut[i+1:]
	}
	return "… (earlier output omitted)\n" + cut
}

// Settings is everything a caller needs to run the project's tests: where the
// project is, and how the run is bounded.
type Settings struct {
	Dir    string
	Config Config
}
