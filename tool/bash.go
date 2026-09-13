package tool

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

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

			// Plan mode: block write operations
			if types.PlanWriteRestricted(ctx) {
				if err := checkPlanModeWrite(cmdStr); err != nil {
					tlog.Warn("shell.bash", "plan_mode_blocked", "command", cmdStr, "reason", err.Error())
					return fmt.Sprintf("\n[PLAN MODE BLOCKED] %s\n\nPlan mode does not allow file modifications. "+
						"Only read-only commands (ls, find, grep, cat, echo without redirect, etc.) are permitted.\n"+
						"Switch to build mode to execute this command.", err), nil
				}
			}

			// Layer 1: Command blocklist check
			if err := DefaultSandbox.CheckCommand(cmdStr); err != nil {
				tlog.Warn("shell.bash", "blocked", "command", cmdStr, "reason", err.Error())
				return fmt.Sprintf("\n[SECURITY BLOCKED] %s\n\nThis command has been blocked by the security policy.\nTell the user this command was blocked and ask what to do instead.", err), nil
			}

			tlog.Info("shell.bash", "exec", "command", cmdStr)

			timeout := 30
			if t, ok := args["timeout"].(float64); ok {
				timeout = int(t)
			}

			cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
			defer cancel()

			cmd := exec.CommandContext(cmdCtx, "bash", "-c", cmdStr)
			// Run the shell in its own process group and kill the whole group
			// on timeout, so background children do not survive the call.
			configureProcessGroup(cmd)
			cmd.Cancel = func() error {
				if cmd.Process == nil {
					return nil
				}
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

			err := cmd.Run()

			var sb strings.Builder
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
