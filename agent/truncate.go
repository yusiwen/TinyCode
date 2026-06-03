package agent

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// TruncMaxLines is the max lines of tool output to return to the LLM.
	TruncMaxLines = 2000

	// TruncMaxBytes is the max bytes of tool output to return to the LLM.
	TruncMaxBytes = 50 * 1024 // 50KB

	// truncDir is where full truncated output files are saved.
	truncDir = "/tmp/tinycode/truncated"
)

// TruncationResult holds the result of truncating a tool's output.
type TruncationResult struct {
	// Content is what to return to the LLM (truncated preview + hint, or full output).
	Content string
	// FullPath is the path to the saved full output, empty if not truncated.
	FullPath string
}

// TruncateOutput truncates tool output that exceeds limits.
// If truncation occurs, the full output is saved to a unique file and the
// returned Content contains a preview plus instructions for reading the rest.
func TruncateOutput(output string) TruncationResult {
	lines := strings.Split(output, "\n")
	if len(lines) <= TruncMaxLines && len(output) <= TruncMaxBytes {
		return TruncationResult{Content: output}
	}

	// Save full output to a unique file
	filePath := uniqueToolFile()
	if wErr := os.WriteFile(filePath, []byte(output), 0644); wErr != nil {
		// If we can't save, just return full output as-is
		return TruncationResult{Content: output}
	}

	// Build head-only preview respecting both limits
	var preview strings.Builder
	bytesAccum := 0
	lineLimit := TruncMaxLines
	if len(lines) < lineLimit {
		lineLimit = len(lines)
	}
	for i := 0; i < lineLimit; i++ {
		lineSize := len(lines[i]) + 1 // +1 for the newline
		if bytesAccum+lineSize > TruncMaxBytes {
			break
		}
		preview.WriteString(lines[i])
		preview.WriteString("\n")
		bytesAccum += lineSize
	}

	totalBytes := len(output)
	removedBytes := totalBytes - bytesAccum

	hint := fmt.Sprintf(
		"\n... (%d bytes truncated) ...\n\n"+
			"[TRUNCATED] Full output saved to: %s\n"+
			"Use read_file with offset/limit to view specific sections.\n",
		removedBytes, filePath,
	)
	preview.WriteString(hint)
	return TruncationResult{Content: preview.String(), FullPath: filePath}
}

// uniqueToolFile generates a unique file path for saving truncated tool output.
func uniqueToolFile() string {
	os.MkdirAll(truncDir, 0755)
	return filepath.Join(truncDir,
		fmt.Sprintf("tool_%x_%x", time.Now().UnixNano(), rand.Uint64()))
}
