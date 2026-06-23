package tool

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yusiwen/tinycode/lsp"
	"github.com/yusiwen/tinycode/tlog"
)

// ReadFile returns a Tool that reads file contents.
func ReadFile() Tool {
	return Tool{
		Name:        "read_file",
		Description: "Read the contents of a file. Returns file content with line numbers. Truncated to 2000 lines if the file is larger. Use offset to read beyond the 2000-line limit (e.g., offset=2001 reads from line 2001), and limit to control how many lines to return.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute or relative path to the file",
				},
				"offset": map[string]any{
					"type":        "integer",
					"description": "Starting line number (1-based). Use this to read beyond the 2000-line truncation limit. Default: 1.",
				},
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of lines to return (default: 2000, max: 2000).",
				},
			},
			"required": []string{"path"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}

			// Layer 2: Path restriction check (with Pattern C interactive prompt)
			if err := DefaultSandbox.CheckPath(path); err != nil {
				if ad, ok := err.(*AccessDenied); ok {
					allowed, mode := RequestPermission(ctx, ad.Path)
					if allowed {
						DefaultSandbox.AllowAlways(path)
						// User approved — fall through to read below
					} else if mode == "cancelled" {
						return "", fmt.Errorf("read cancelled")
					} else {
						return ad.DenyHint(), nil
					}
				} else {
					return "", fmt.Errorf("path check: %w", err)
				}
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("read %s: %w", path, err)
			}

			const maxLines = 2000
			lines := strings.Split(string(data), "\n")
			totalLines := len(lines)

			startLine := 1
			if offset, ok := args["offset"].(float64); ok && offset > 1 {
				startLine = int(offset)
			}

			readLimit := maxLines
			if l, ok := args["limit"].(float64); ok && l > 0 {
				readLimit = int(l)
				if readLimit > maxLines {
					readLimit = maxLines
				}
			}

			// Convert from 1-based to 0-based slice indices
			startIdx := startLine - 1
			if startIdx >= totalLines {
				return fmt.Sprintf("=== %s (requested offset %d but file has only %d lines) ===\n", path, startLine, totalLines), nil
			}

			endIdx := startIdx + readLimit
			if endIdx > totalLines {
				endIdx = totalLines
			}

			slice := lines[startIdx:endIdx]
			actualLines := len(slice)

			var sb strings.Builder
			if startLine == 1 && actualLines == totalLines {
				sb.WriteString(fmt.Sprintf("=== %s (%d lines) ===\n", path, totalLines))
			} else {
				sb.WriteString(fmt.Sprintf("=== %s (%d lines, showing %d-%d) ===\n", path, totalLines, startLine, startLine+actualLines-1))
			}
			for i, line := range slice {
				sb.WriteString(fmt.Sprintf("%5d| %s\n", startLine+i, line))
			}
			if endIdx < totalLines {
				sb.WriteString(fmt.Sprintf("... (truncated, file has %d lines total; use offset=%d to read more)\n", totalLines, endIdx+1))
			}

			tlog.Debug("fs.read", "done", "file", path, "lines", actualLines, "total", totalLines, "offset", startLine)

			// LSP warmup (fire-and-forget, non-blocking)
			if lsp.IsAvailable() {
				go lsp.TouchFile(path, false)
			}

			return sb.String(), nil
		},
	}
}

// WriteFile returns a Tool that writes content to a file.
func WriteFile() Tool {
	return Tool{
		Name:        "write_file",
		Description: "Write content to a file. Creates parent directories if needed. Overwrites existing content.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute or relative path to the file",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Full content to write",
				},
			},
			"required": []string{"path", "content"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			content, _ := args["content"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}

			// Layer 2: Path restriction check (with Pattern C interactive prompt)
			if err := DefaultSandbox.CheckPath(path); err != nil {
				if ad, ok := err.(*AccessDenied); ok {
					allowed, mode := RequestPermission(ctx, ad.Path)
					if allowed {
						DefaultSandbox.AllowAlways(path)
						// User approved — fall through to write below
					} else if mode == "cancelled" {
						return "", fmt.Errorf("write cancelled")
					} else {
						return ad.DenyHint(), nil
					}
				} else {
					return "", fmt.Errorf("path check: %w", err)
				}
			}

			if lsp.IsAvailable() {
				// Snapshot baseline BEFORE write
				lsp.SnapshotBaseline(path)
			}

			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return "", fmt.Errorf("mkdir: %w", err)
			}

			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return "", fmt.Errorf("write %s: %w", path, err)
			}

			result := fmt.Sprintf("Wrote %d bytes to %s", len(content), path)
			tlog.Info("fs.write", "done", "file", path, "bytes", len(content))

			// LSP diagnostics: only new errors introduced by this edit
			if lsp.IsAvailable() {
				if newDiags := lsp.GetNewDiagnostics(path); len(newDiags) > 0 {
					result += lsp.FormatDiagnostics(path, newDiags)
				}
			}

			return result, nil
		},
	}
}

// SearchFiles returns a Tool that searches file contents.
// Uses a priority ladder: ripgrep (fastest) → grep (universal on Linux) → Go native (portable).
func SearchFiles() Tool {
	return Tool{
		Name:        "search_files",
		Description: "Search for text patterns in files. Supports regex. Returns matching lines with file paths and line numbers. Searches recursively.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]any{
					"type":        "string",
					"description": "Regex pattern to search for",
				},
				"path": map[string]any{
					"type":        "string",
					"description": "Directory or file to search in (default: current directory)",
				},
				"file_glob": map[string]any{
					"type":        "string",
					"description": "File pattern to filter (e.g. *.go, *.py, *.md)",
				},
			},
			"required": []string{"pattern"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			pattern, _ := args["pattern"].(string)
			if pattern == "" {
				return "", fmt.Errorf("pattern is required")
			}

			searchPath := "."
			if p, ok := args["path"].(string); ok && p != "" {
				searchPath = p
			}

			glob, _ := args["file_glob"].(string)

			// Priority ladder: rg → grep → Go native
			var searchResult string
			var searchErr error
			if isAvailable("rg") {
				searchResult, searchErr = searchWithRG(ctx, pattern, searchPath, glob)
			} else if isAvailable("grep") {
				searchResult, searchErr = searchWithGrep(ctx, pattern, searchPath, glob)
			} else {
				searchResult, searchErr = searchGoNative(ctx, pattern, searchPath, glob)
			}
			if searchErr != nil {
				return searchResult, searchErr
			}
			tlog.Debug("fs.search", "done", "pattern", pattern, "path", searchPath, "result_len", len(searchResult))
			return searchResult, nil
		},
	}
}

// isAvailable checks if a command exists in PATH.
func isAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// searchWithRG uses ripgrep (fastest).
func searchWithRG(ctx context.Context, pattern, searchPath, glob string) (string, error) {
	args := []string{"--line-number", "--heading", "--color", "never"}
	if glob != "" {
		args = append(args, "--glob", glob)
	}
	args = append(args, pattern, searchPath)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// ripgrep exits with code 1 when no matches found — not an error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "No matches found.", nil
		}
		return "", fmt.Errorf("rg failed: %w\n%s", err, stderr.String())
	}

	if stdout.Len() == 0 {
		return "No matches found.", nil
	}
	return strings.TrimSpace(stdout.String()), nil
}

// searchWithGrep uses grep -rn (universal on Linux).
func searchWithGrep(ctx context.Context, pattern, searchPath, glob string) (string, error) {
	// If a file glob is specified, we need to use --include
	args := []string{"-rn", "--color=never"}
	if glob != "" {
		args = append(args, "--include", glob)
	}
	args = append(args, pattern, searchPath)

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "grep", args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "No matches found.", nil
		}
		return "", fmt.Errorf("grep failed: %w\n%s", err, stderr.String())
	}

	if stdout.Len() == 0 {
		return "No matches found.", nil
	}
	return strings.TrimSpace(stdout.String()), nil
}

// searchGoNative is the portable fallback using Go stdlib.
func searchGoNative(ctx context.Context, pattern, searchPath, glob string) (string, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex %q: %w", pattern, err)
	}

	var globRe *regexp.Regexp
	if glob != "" {
		// Convert glob (e.g. *.go) to regex
		globPattern := strings.ReplaceAll(glob, ".", "\\.")
		globPattern = strings.ReplaceAll(globPattern, "*", ".*")
		globRe, err = regexp.Compile("^" + globPattern + "$")
		if err != nil {
			return "", fmt.Errorf("invalid glob %q: %w", glob, err)
		}
	}

	var sb strings.Builder
	matchCount := 0
	fileCount := 0

	err = filepath.Walk(searchPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible files
		}
		if info.IsDir() {
			// Skip hidden directories
			if strings.HasPrefix(info.Name(), ".") && info.Name() != "." {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip hidden files
		if strings.HasPrefix(info.Name(), ".") {
			return nil
		}

		// Apply file glob filter
		if globRe != nil && !globRe.MatchString(info.Name()) {
			return nil
		}

		// Skip binary files by checking first few bytes
		isBinary := false
		f, err := os.Open(path)
		if err == nil {
			buf := make([]byte, 512)
			n, _ := f.Read(buf)
			f.Close()
			isBinary = hasNullByte(buf[:n])
		}
		if isBinary {
			return nil
		}

		f, err = os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		lineNum := 0
		fileMatches := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				if fileMatches == 0 {
					sb.WriteString(fmt.Sprintf("%s\n", path))
					fileCount++
				}
				sb.WriteString(fmt.Sprintf("%d:%s\n", lineNum, line))
				fileMatches++
				matchCount++
			}
		}

		return nil
	})

	if err != nil {
		return "", fmt.Errorf("walk failed: %w", err)
	}

	if matchCount == 0 {
		return "No matches found.", nil
	}

	summary := fmt.Sprintf("Found %d matches in %d files:\n\n", matchCount, fileCount)
	return summary + strings.TrimSpace(sb.String()), nil
}

// hasNullByte checks if a byte slice contains a null byte (binary file heuristic).
func hasNullByte(buf []byte) bool {
	for _, b := range buf {
		if b == 0 {
			return true
		}
	}
	return false
}
