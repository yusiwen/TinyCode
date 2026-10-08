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

// checkPlanModeWrite returns an error if the command contains write operations.
func checkPlanModeWrite(cmd string) error {
	trimmed := strings.TrimSpace(cmd)

	// Check dangerous commands at start of command or after &&/||/;
	writeCommands := []string{
		"mkdir", "rmdir", "rm", "mv", "cp", "dd", "chmod", "chown", "ln",
		"install", "touch", "truncate",
		"mkfs", "mount", "umount",
	}
	for _, wc := range writeCommands {
		if strings.HasPrefix(trimmed, wc+" ") || strings.HasPrefix(trimmed, wc+"\t") {
			return fmt.Errorf("write command '%s' is not allowed in plan mode", wc)
		}
	}
	// Same check after common operators
	for _, wc := range writeCommands {
		patterns := []string{"&& " + wc + " ", "|| " + wc + " ", "; " + wc + " ", "| " + wc + " "}
		for _, p := range patterns {
			if strings.Contains(cmd, p) {
				return fmt.Errorf("write command '%s' is not allowed in plan mode", wc)
			}
		}
	}

	// Check output redirection to files (not /dev/null or pipes)
	lines := strings.Split(trimmed, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Heredoc pattern: << TERMINATOR
		if containsHeredoc(line) {
			return fmt.Errorf("heredoc (cat <<) is not allowed in plan mode — use write_file in build mode")
		}

		// Redirect to regular file (allow &>/dev/null, 2>/dev/null, | pipe)
		if containsFileRedirect(line) {
			return fmt.Errorf("output redirection to file (>) is not allowed in plan mode")
		}
	}

	return nil
}

// containsHeredoc checks if the line contains a heredoc operator.
func containsHeredoc(line string) bool {
	// << followed by something that's not a digit or whitespace
	for i := 0; i < len(line)-1; i++ {
		if line[i] == '<' && line[i+1] == '<' {
			// Make sure this isn't <<< (here-string)
			if i+2 < len(line) && line[i+2] == '<' {
				continue
			}
			return true
		}
	}
	return false
}

// containsFileRedirect checks if the line redirects output to a file.
func containsFileRedirect(line string) bool {
	// Remove quoted strings to avoid false positives
	simplified := removeQuoted(line)

	for i := 0; i < len(simplified); i++ {
		if simplified[i] == '>' {
			// Check if it's a comparison operator (like -gt, or in test [])
			if i > 0 && (simplified[i-1] == '-' || simplified[i-1] == '=' || simplified[i-1] == '!') {
				continue
			}
			// Check if it's >> (append) — also a write
			// Check if it's >& (redirect to fd) — we allow &>/dev/null
			if i+1 < len(simplified) && simplified[i+1] == '&' {
				rest := strings.TrimSpace(simplified[i+2:])
				if strings.HasPrefix(rest, "/dev/null") || strings.HasPrefix(rest, "-") {
					continue // allow &>/dev/null and >&- (close fd)
				}
			}
			// Check if it's > followed by /dev/null
			rest := strings.TrimSpace(simplified[i+1:])
			if strings.HasPrefix(rest, "/dev/null") {
				continue // allow > /dev/null
			}
			// Check if it's > followed by a file descriptor number (like 2>/dev/null handled above)
			// Remaining redirect to file: block it
			if i+1 < len(simplified) {
				next := strings.TrimSpace(simplified[i+1:])
				if len(next) > 0 && !strings.HasPrefix(next, "&") && !strings.HasPrefix(next, "/dev/null") && next[0] != ' ' {
					return true
				}
			}
		}
	}
	return false
}

// removeQuoted strips content inside quotes for simplified analysis.
func removeQuoted(s string) string {
	var result []byte
	inSingle := false
	inDouble := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if s[i] == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if !inSingle && !inDouble {
			result = append(result, s[i])
		}
	}
	return string(result)
}

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

			// Plan mode: block write operations. The run's policy says which mode
			// this is; the guard does not infer it from configuration.
			if policy := runPolicy(ctx); policy.Mode == types.SandboxReadOnly {
				if err := checkPlanModeWrite(cmdStr); err != nil {
					tlog.Warn("shell.bash", "plan_mode_blocked", "command", cmdStr, "reason", err.Error())
					return types.Refusal{
						Subject: fmt.Sprintf("command %q", cmdStr),
						Reason:  "would modify files, which plan mode does not allow",
						Mode:    policy.Mode,
						Ask:     types.AskMode,
						Detail:  []string{fmt.Sprintf("rule: %v", err)},
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
