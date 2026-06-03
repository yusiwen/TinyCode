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
)

// ReadFile returns a Tool that reads file contents.
func ReadFile() Tool {
	return Tool{
		Name:        "read_file",
		Description: "Read the contents of a file. Returns file content with line numbers.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute or relative path to the file",
				},
			},
			"required": []string{"path"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}

			// Layer 2: Path restriction check
			if err := DefaultSandbox.CheckPath(path); err != nil {
				if ad, ok := err.(*AccessDenied); ok {
					return ad.DenyHint(), nil
				}
				return "", fmt.Errorf("path check: %w", err)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("read %s: %w", path, err)
			}

			lines := strings.Split(string(data), "\n")
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("=== %s (%d lines) ===\n", path, len(lines)))
			for i, line := range lines {
				sb.WriteString(fmt.Sprintf("%5d| %s\n", i+1, line))
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

			// Layer 2: Path restriction check
			if err := DefaultSandbox.CheckPath(path); err != nil {
				if ad, ok := err.(*AccessDenied); ok {
					return ad.DenyHint(), nil
				}
				return "", fmt.Errorf("path check: %w", err)
			}

			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				return "", fmt.Errorf("mkdir: %w", err)
			}

			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return "", fmt.Errorf("write %s: %w", path, err)
			}

			return fmt.Sprintf("Wrote %d bytes to %s", len(content), path), nil
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
			if isAvailable("rg") {
				return searchWithRG(ctx, pattern, searchPath, glob)
			}
			if isAvailable("grep") {
				return searchWithGrep(ctx, pattern, searchPath, glob)
			}
			return searchGoNative(ctx, pattern, searchPath, glob)
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
