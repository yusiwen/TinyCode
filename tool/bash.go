package tool

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// bashMaxLines is the max lines of bash output to return to the LLM.
	// Beyond this, output is truncated and saved to a file.
	bashMaxLines = 2000

	// bashMaxBytes is the max bytes of bash output to return to the LLM.
	bashMaxBytes = 50 * 1024 // 50KB

	// bashTruncDir is where full truncated output files are saved.
	bashTruncDir = "/tmp/tinycode/truncated"
)

// uniqueToolFile generates a unique file path for saving truncated tool output.
func uniqueToolFile() string {
	os.MkdirAll(bashTruncDir, 0755)
	return filepath.Join(bashTruncDir,
		fmt.Sprintf("tool_%x_%x", time.Now().UnixNano(), rand.Uint64()))
}

// Bash returns a Tool that executes shell commands.
func Bash() Tool {
	return Tool{
		Name:        "bash",
		Description: "Execute a shell command and return its combined stdout+stderr. " +
			"Use this to run commands, build code, run tests, install packages, etc. " +
			"Output is truncated at 2000 lines or 50 KB; use read_file with offset/limit to view the full saved output.",
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
				return fmt.Sprintf("\n[SECURITY BLOCKED] %s\n\nThis command has been blocked by the security policy.\nTell the user this command was blocked and ask what to do instead.", err), nil
			}

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
				return result, nil
			}

			// Truncation check: limit by lines or bytes
			lines := strings.Split(result, "\n")
			if len(lines) <= bashMaxLines && len(result) <= bashMaxBytes {
				return result, nil
			}

			// Save full output to a unique file
			filePath := uniqueToolFile()
			if wErr := os.WriteFile(filePath, []byte(result), 0644); wErr != nil {
				// If we can't save, just return full output as-is
				return result, nil
			}

			// Build head-only preview respecting both limits
			var preview strings.Builder
			bytesAccum := 0
			lineLimit := bashMaxLines
			if len(lines) < lineLimit {
				lineLimit = len(lines)
			}
			for i := 0; i < lineLimit; i++ {
				lineSize := len(lines[i]) + 1 // +1 for the newline
				if bytesAccum+lineSize > bashMaxBytes {
					break
				}
				preview.WriteString(lines[i])
				preview.WriteString("\n")
				bytesAccum += lineSize
			}

			totalBytes := len(result)
			removedBytes := totalBytes - bytesAccum

			hint := fmt.Sprintf(
				"\n... (%d bytes truncated) ...\n\n"+
					"[TRUNCATED] Full output saved to: %s\n"+
					"Use read_file with offset/limit to view specific sections.\n",
				removedBytes, filePath,
			)
			preview.WriteString(hint)
			return preview.String(), nil
		},
	}
}
