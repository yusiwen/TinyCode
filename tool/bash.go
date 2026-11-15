package tool

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// exitCodeOf reports the exit status a finished command failed with, if it did.
func exitCodeOf(err error) (int, bool) {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}
	return 0, false
}

// maxOutputBytes caps how much of each stream (stdout/stderr) is retained, so
// a runaway command cannot exhaust the agent's memory.
const maxOutputBytes = 1 << 20 // 1 MiB per stream

// limitedBuffer is a bytes.Buffer that silently stops storing data once the
// limit is reached, while still reporting the full write length so the child
// process keeps running instead of seeing a short write.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	// A zero limit means "use the default cap", never "unlimited".
	limit := b.limit
	if limit <= 0 {
		limit = maxOutputBytes
	}
	if b.buf.Len() >= limit {
		b.truncated = true
		return len(p), nil
	}
	room := limit - b.buf.Len()
	if len(p) > room {
		b.buf.Write(p[:room])
		b.truncated = true
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) Len() int       { return b.buf.Len() }
func (b *limitedBuffer) String() string { return b.buf.String() }

func Bash() Tool {
	return Tool{
		Name: "bash",
		Description: "Execute a shell command and return its combined stdout+stderr. " +
			"Use this to run commands, build code, run tests, install packages, etc.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]any{
					"type":        "string",
					"description": "The shell command to execute",
				},
				"timeout": map[string]any{
					"type":        "number",
					"description": "Timeout in seconds (default: 30)",
				},
				"workdir": map[string]any{
					"type":        "string",
					"description": "Working directory (default: current)",
				},
			},
			"required": []string{"command"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			cmdStr, _ := args["command"].(string)
			if cmdStr == "" {
				return "", fmt.Errorf("command is required")
			}

			// Plan mode is a boundary or it is nothing. A read-only run runs its
			// commands inside the kernel or container boundary, and refuses to run
			// them at all when neither is available: a string check cannot tell
			// what a shell command will do, and the one thing a read-only mode must
			// not do is promise a guarantee it cannot keep.
			if policy := runPolicy(ctx); policy.Mode == types.SandboxReadOnly {
				if ok, missing := readOnlyBoundary(policy); !ok {
					tlog.Warn("shell.bash", "plan_mode_refused", "command", cmdStr, "missing", missing)
					return types.Refusal{
						Subject: fmt.Sprintf("command %q", cmdStr),
						Reason:  "plan mode runs commands only inside a boundary it can enforce, and this run has none",
						Mode:    policy.Mode,
						Ask:     types.AskMode,
						Detail: []string{
							"missing: " + missing,
							"a string check is not a boundary: a shell can build any command at run time",
						},
					}.Message(), nil
				}
				// The boundary would refuse the write anyway. This refusal says why
				// before the kernel says "operation not permitted", and it covers
				// only the commands it can read; anything else falls through to the
				// boundary, which is the authority.
				if hint := readOnlyWriteHint(cmdStr); hint != "" {
					tlog.Warn("shell.bash", "plan_mode_blocked", "command", cmdStr, "reason", hint)
					return types.Refusal{
						Subject: fmt.Sprintf("command %q", cmdStr),
						Reason:  "would modify files, which plan mode does not allow",
						Mode:    policy.Mode,
						Ask:     types.AskMode,
						Detail:  []string{hint},
					}.Message(), nil
				}
			}

			// Layer 1: Command blocklist check
			if err := DefaultSandbox.CheckCommand(cmdStr); err != nil {
				tlog.Warn("shell.bash", "blocked", "command", cmdStr, "reason", err.Error())
				return types.Refusal{
					Subject: fmt.Sprintf("command %q", cmdStr),
					Reason:  "blocked by the configured command policy",
					Mode:    runPolicy(ctx).Mode,
					Ask:     types.AskPolicy,
					Detail:  []string{fmt.Sprintf("rule: %v", err)},
				}.Message(), nil
			}

			tlog.Info("shell.bash", "exec", "command", cmdStr)

			// The command's effects are recorded from a snapshot of the tree taken
			// before it starts, because a shell command never says what it will
			// touch (issue #238). It is taken before the command's own timeout
			// clock, so our overhead is not spent out of the model's budget, and
			// finished by a defer on every path: a killed command's changes are the
			// ones an undo is most likely to be wanted for.
			snap := beginCommandSnapshot(ctx)
			if snap != nil {
				defer snap.finish(ctx)
			}

			timeout := 30
			if t, ok := args["timeout"].(float64); ok {
				timeout = int(t)
			}

			cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
			defer cancel()

			// Confinement wraps the same ['bash','-c',cmd] argv, so the command
			// the caller asked for is unchanged; only what enforces its file
			// effects differs.
			argv, invErr := bashInvocation(ctx, cmdStr)
			if invErr != nil {
				return "", invErr
			}
			cmd := exec.CommandContext(cmdCtx, argv[0], argv[1:]...)
			// A confined command cannot write the shared temp area, but a
			// toolchain still needs scratch (the go command makes its work
			// directory under $TMPDIR), so point it at the per-user scratch
			// inside the granted cache root. Unconfined runs inherit the
			// environment unchanged.
			if ConfineCommands() {
				if env := confinedEnv(runPolicy(ctx)); env != nil {
					cmd.Env = env
				}
			}
			// Run the shell in its own process group and kill the whole group
			// on timeout, so background children do not survive the call.
			configureProcessGroup(cmd)

			// On Linux the command also runs inside its own cgroup v2 group: a
			// descendant that double-forks re-parents to init and leaves the
			// process group, but it cannot leave the group it inherited, so
			// cgroup.kill (or killing every pid in cgroup.procs) still reaches it
			// (issue #9). No delegation → the portable kill below, as before.
			cg, cgErr := newBashCgroup()
			if cgErr != nil {
				tlog.Debug("shell.bash", "cgroup_unavailable", "err", cgErr.Error())
			}
			if cg != nil {
				defer func() { _ = cg.remove() }()
			}

			// Which mechanism actually killed a timed-out command, for the report
			// line appended below.
			var killedBy atomic.Value

			cmd.Cancel = func() error {
				if cmd.Process == nil {
					return nil
				}
				// Both, not either: the cgroup reaches the double-forked
				// survivor, and the group/tree walk is the portable path that
				// still works if the pid was not moved into the group yet (the
				// timeout can fire between Start and add).
				mechanism := "process group + tree walk"
				if cg != nil {
					if err := cg.kill(); err != nil {
						tlog.Warn("shell.bash", "cgroup_kill_failed", "dir", cg.dir, "err", err.Error())
					} else {
						mechanism = "cgroup v2 + process group"
					}
				}
				killedBy.Store(mechanism)
				return killProcessGroup(cmd.Process.Pid)
			}
			// Give the I/O copiers a short grace period after the kill so a
			// stubborn grandchild holding the pipes cannot block Wait forever.
			cmd.WaitDelay = 5 * time.Second

			if wd, ok := args["workdir"].(string); ok && wd != "" {
				cmd.Dir = wd
			} else if root := rootFor(ctx); root != "" {
				// A delegated task can run with its own root (roadmap C3): a
				// command with no explicit workdir belongs to that root, not to
				// the process's working directory.
				cmd.Dir = root
			}

			var stdout, stderr limitedBuffer
			stdout.limit = maxOutputBytes
			stderr.limit = maxOutputBytes
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			var err error
			if startErr := cmd.Start(); startErr != nil {
				err = startErr
			} else {
				if cg != nil {
					// The window between Start and this call is the only moment a
					// process can escape the group: a child forked before the move
					// stays where it was.
					if addErr := cg.add(cmd.Process.Pid); addErr != nil {
						tlog.Warn("shell.bash", "cgroup_add_failed", "dir", cg.dir, "err", addErr.Error())
					}
				}
				err = cmd.Wait()
			}

			var sb strings.Builder

			// A launcher failure is not a command failure: the command never
			// ran, and reporting it as an ordinary error would send the reader
			// looking for a bug in their command. Both the exit status and our
			// own diagnostic prefix must match, so a command that exits 125 by
			// itself is not read as a sandbox report.
			if ConfineCommands() {
				if code, ok := exitCodeOf(err); ok && SandboxLauncherFailed(code, stderr.String()) {
					tlog.Warn("shell.bash", "sandbox_launcher_failed", "exit", code)
					return types.Refusal{
						Subject: fmt.Sprintf("command %q", cmdStr),
						Reason:  "the file boundary could not be applied on this host, so the command did not run",
						Mode:    runPolicy(ctx).Mode,
						Ask:     types.AskCapability,
						Detail:  SandboxLauncherDiagnostics(stderr.String()),
					}.Message(), nil
				}
			}

			if stdout.Len() > 0 {
				sb.WriteString("STDOUT:\n")
				sb.WriteString(stdout.String())
				sb.WriteString("\n")
			}
			if stderr.Len() > 0 {
				sb.WriteString("STDERR:\n")
				sb.WriteString(stderr.String())
				sb.WriteString("\n")
			}
			if stdout.truncated || stderr.truncated {
				sb.WriteString(fmt.Sprintf("[output truncated at %d bytes per stream]\n", maxOutputBytes))
			}
			// Only the negative facts are reported: a command whose changes were
			// fully recorded says nothing extra, while one that could not be
			// recorded says so here rather than leaving the reader to assume /undo
			// covers it.
			if note := snap.note(); note != "" {
				sb.WriteString(note + "\n")
			}

			if err != nil {
				sb.WriteString(fmt.Sprintf("ERROR: %v\n", err))
				if cmdCtx.Err() != nil {
					// A timeout is the one case where the kill mechanism is worth
					// reporting: "process group + tree walk" means a double-forked
					// descendant may have survived it.
					mechanism, _ := killedBy.Load().(string)
					sb.WriteString(killNote(timeout, mechanism) + "\n")
				}
			}

			result := strings.TrimSpace(sb.String())
			tlog.Debug("shell.bash", "result", "output_size", len(result), "exit_error", err != nil)
			if result == "" {
				result = "(no output)"
			}
			return result, nil
		},
	}
}
