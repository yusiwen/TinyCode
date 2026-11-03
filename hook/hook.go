// Package hook runs the commands a user configured to fire at the agent's tool
// and run boundaries (roadmap C4).
//
// A hook is a shell command, so this package is mostly about the limits: it is
// bounded by a timeout, its output is truncated, and for PreToolUse a hook that
// fails to run or times out denies the call rather than silently passing it.
package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yusiwen/tinycode/config"
)

// Event names a point a hook can run at.
type Event string

const (
	// PreToolUse runs before a tool executes; a refusal denies the call.
	PreToolUse Event = "pre_tool_use"
	// PostToolUse runs after a tool returned and cannot change its result.
	PostToolUse Event = "post_tool_use"
	// Stop runs when a run returns, whatever its outcome.
	Stop Event = "stop"
	// SessionEnd runs once when the process is done with the session.
	SessionEnd Event = "session_end"
)

// Events lists the events in the order they can fire, for a view that wants to
// show them all.
func Events() []Event { return []Event{PreToolUse, PostToolUse, Stop, SessionEnd} }

// maxHookOutput bounds what one hook can add to a transcript. A hook that prints
// megabytes is a hook that broke its own contract, and the model should not pay
// for it.
const maxHookOutput = 2000

// Payload is what a hook receives on stdin as one JSON object.
type Payload struct {
	Event   string `json:"event"`
	Tool    string `json:"tool,omitempty"`
	Args    string `json:"args,omitempty"`
	Result  string `json:"result,omitempty"`
	Error   string `json:"error,omitempty"`
	Session string `json:"session,omitempty"`
	Step    int    `json:"step,omitempty"`
}

// Hook is one configured command, already bounded.
type Hook struct {
	Command string
	Timeout time.Duration
	Match   string
}

// Result is what firing one event produced.
type Result struct {
	// Denied is set when a PreToolUse hook refused the call.
	Denied bool
	// Reason is the denial's text: the hook's own output when it printed one.
	Reason string
	// Output is the concatenated, truncated stdout/stderr of the hooks that ran.
	Output string
	// Ran is how many hooks ran (a hook skipped by its match does not count).
	Ran int
	// Err is the first hook failure that was not a denial. A failed hook is
	// reported here rather than dropped: a hook that does not run is a fact the
	// user needs.
	Err error
}

// Runner fires the hooks of one session.
type Runner struct {
	hooks map[Event][]Hook
	// Dir is the working directory of every hook: the project root, so a hook
	// like `gofmt -w .` means what it says.
	Dir string
	// sessionEndOnce makes SessionEnd fire at most once per process: every exit
	// path calls it, and only the first call runs the hooks.
	sessionEndOnce sync.Once
}

// New builds a runner from the resolved config. Nil lists mean no hooks.
func New(hooks config.HooksConfig, dir string) *Runner {
	return &Runner{
		hooks: map[Event][]Hook{
			PreToolUse:  convert(hooks.PreToolUse),
			PostToolUse: convert(hooks.PostToolUse),
			Stop:        convert(hooks.Stop),
			SessionEnd:  convert(hooks.SessionEnd),
		},
		Dir: dir,
	}
}

func convert(list []config.HookConfig) []Hook {
	out := make([]Hook, 0, len(list))
	for _, entry := range list {
		out = append(out, Hook{Command: entry.Command, Timeout: entry.Timeout(), Match: entry.Match})
	}
	return out
}

// Empty reports whether no hook is configured at all.
func (r *Runner) Empty() bool {
	if r == nil {
		return true
	}
	for _, list := range r.hooks {
		if len(list) > 0 {
			return false
		}
	}
	return true
}

// Hooks returns the configured hooks of one event, for a listing.
func (r *Runner) Hooks(event Event) []Hook {
	if r == nil {
		return nil
	}
	return r.hooks[event]
}

// FireSessionEnd runs the SessionEnd hooks once per process, whatever the exit
// path. A second call does nothing: a session ends once, and a hook that
// exported the transcript must not run twice because the TUI has three ways out.
func (r *Runner) FireSessionEnd(session string) Result {
	if r == nil {
		return Result{}
	}
	var result Result
	r.sessionEndOnce.Do(func() {
		result = r.Fire(context.Background(), SessionEnd, Payload{Session: session})
	})
	return result
}

// Fire runs every hook of the event whose match accepts the payload.
//
// PreToolUse treats a non-zero exit, a failed spawn and a timeout as a denial;
// the other events report the failure in Err and keep going, because a formatter
// that breaks must not stop the agent from working.
func (r *Runner) Fire(ctx context.Context, event Event, payload Payload) Result {
	var result Result
	if r == nil {
		return result
	}
	payload.Event = string(event)
	for _, h := range r.hooks[event] {
		if !h.matches(payload.Tool) {
			continue
		}
		result.Ran++
		out, err := r.run(ctx, h, payload)
		if out != "" {
			if result.Output != "" {
				result.Output += "\n"
			}
			result.Output += out
		}
		if err == nil {
			continue
		}
		if event == PreToolUse {
			result.Denied = true
			result.Reason = err.Error()
			// The first refusal is the answer: later hooks cannot un-deny it.
			return result
		}
		if result.Err == nil {
			result.Err = err
		}
	}
	return result
}

// matches applies the hook's glob to a tool name. An empty match is "every
// tool"; a pattern that does not parse matches nothing, so a typo in a hook's
// match cannot widen it.
func (h Hook) matches(tool string) bool {
	if h.Match == "" || h.Match == "*" {
		return true
	}
	ok, err := filepath.Match(h.Match, tool)
	return err == nil && ok
}

// run executes one hook with its timeout and returns its truncated output. The
// error is a non-zero exit (with the output as the reason), a spawn failure, or a
// timeout — all of which are refusals for PreToolUse.
func (r *Runner) run(ctx context.Context, h Hook, payload Payload) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("hook payload: %w", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, h.Timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, "sh", "-c", h.Command)
	cmd.Dir = r.Dir
	cmd.Stdin = bytes.NewReader(data)
	// Killing the shell is not enough to end the call: a child it spawned (a
	// `sleep`, a compiler) inherits the output pipe, and Wait would sit on it
	// until the child finished — which is exactly the timeout it was supposed to
	// enforce. WaitDelay bounds that wait after the kill.
	cmd.WaitDelay = 200 * time.Millisecond
	out, err := cmd.CombinedOutput()
	text := truncate(string(out))
	if runCtx.Err() == context.DeadlineExceeded {
		return text, fmt.Errorf("hook %q timed out after %s", h.Command, h.Timeout)
	}
	if err != nil {
		if strings.TrimSpace(text) != "" {
			return text, fmt.Errorf("hook %q failed: %s", h.Command, text)
		}
		return text, fmt.Errorf("hook %q failed: %w", h.Command, err)
	}
	return text, nil
}

// truncate clips a hook's output to what a transcript can afford.
func truncate(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxHookOutput {
		return s
	}
	return s[:maxHookOutput] + "… (hook output truncated)"
}
