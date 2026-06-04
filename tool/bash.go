package tool

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/tlog"
)

// Bash returns a Tool that executes shell commands.
func Bash() Tool {
	return Tool{
		Name:        "bash",
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

			if wd, ok := args["workdir"].(string); ok && wd != "" {
				cmd.Dir = wd
			}

			var stdout, stderr bytes.Buffer
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

			if err != nil {
				sb.WriteString(fmt.Sprintf("ERROR: %v\n", err))
			}

			result := strings.TrimSpace(sb.String())
			if result == "" {
				result = "(no output)"
			}
			return result, nil
		},
	}
}
