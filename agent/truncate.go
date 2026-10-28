package agent

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yusiwen/tinycode/tlog"
)

var (
	// TruncMaxLines is the max lines of tool output to return to the LLM.
	TruncMaxLines = 2000

	// TruncMaxBytes is the max bytes of tool output to return to the LLM.
	// Set high enough (200KB) to pass read_file's max 2000-line output (~100KB)
	// without re-truncating it. Acts as a safety net for tools that don't
	// have their own internal truncation (e.g. bash, code_review).
	TruncMaxBytes = 200 * 1024 // 200KB

	// TruncMaxTokens bounds the preview by what the model actually pays for.
	// It is the bound that matters once text is dense: 200 KiB of it is ~50k
	// tokens, a whole context window spent on one tool result. EstimateTokens
	// is the estimator; bytesPerToken below is its inverse and a test keeps the
	// two in step.
	TruncMaxTokens = 4000

	// TruncTailLines is how many of the output's last lines the preview keeps
	// beside the head. A failure is usually in the last lines, and a head-only
	// preview drops exactly the evidence a reader needs.
	TruncTailLines = 20

	// truncDir is where full truncated output files are saved.
	truncDir = "/tmp/tinycode/truncated"
)

// bytesPerToken is the estimator's ratio: EstimateTokens reports len(text)/4.
// The head loop counts bytes because that is O(1) per line; a test pins this
// inverse to the estimator so the two cannot drift.
const bytesPerToken = 4

// The two signals the omitted-lines summary counts. They are matched, not
// summarised: the count has to be cheap, deterministic and explainable, and a
// model call would make an already-large tool result cost another turn.
var (
	truncErrorSignal   = regexp.MustCompile(`(?i)\b(error|failed|failure|panic|fatal|denied)\b`)
	truncWarningSignal = regexp.MustCompile(`(?i)\bwarn(ing)?\b`)
)

// SetTruncationConfig overrides the output-truncation limits from the
// configuration file. Zero or negative values keep the current setting. Call it
// during startup, before any tool runs.
func SetTruncationConfig(maxLines, maxBytes, maxTokens, tailLines int, outputDir string) {
	if maxLines > 0 {
		TruncMaxLines = maxLines
	}
	if maxBytes > 0 {
		TruncMaxBytes = maxBytes
	}
	if maxTokens > 0 {
		TruncMaxTokens = maxTokens
	}
	if tailLines > 0 {
		TruncTailLines = tailLines
	}
	if outputDir != "" {
		truncDir = outputDir
	}
}

// TruncationResult holds the result of truncating a tool's output.
type TruncationResult struct {
	// Content is what to return to the LLM (truncated preview + hint, or full output).
	Content string
	// FullPath is the path to the saved full output, empty if not truncated.
	FullPath string
	// OmittedLines is how many lines did not fit the preview. Zero when the
	// output fit.
	OmittedLines int
}

// TruncateOutput truncates tool output that exceeds its bounds.
//
// The preview keeps the head, the tail, and a count of what fell between them:
// a head-only slice loses the panic at the end, and a hint that says only "some
// bytes were dropped" cannot tell a reader whether the dropped part mattered.
// The full output is still written to a file, so nothing is lost.
func TruncateOutput(output string) TruncationResult {
	lines := strings.Split(output, "\n")
	// A trailing newline leaves an empty final element; it is an artefact of the
	// split, not a line, and counting it would make every "N lines" number one
	// too high.
	if n := len(lines); n > 0 && lines[n-1] == "" && strings.HasSuffix(output, "\n") {
		lines = lines[:n-1]
	}
	if len(lines) <= TruncMaxLines && len(output) <= TruncMaxBytes && EstimateTokens(output) <= TruncMaxTokens {
		return TruncationResult{Content: output}
	}

	// Save full output to a unique file
	filePath := uniqueToolFile()
	// Tool output can contain secrets (env dumps, tokens), so keep the file
	// readable only by the owner.
	if wErr := os.WriteFile(filePath, []byte(output), 0600); wErr != nil {
		// If we can't save, just return full output as-is
		return TruncationResult{Content: output}
	}

	tlog.Debug("truncate", "truncated", "file", filePath, "total_lines", len(lines),
		"total_bytes", len(output), "total_tokens", EstimateTokens(output))

	// The tail comes first so the budget covers the whole preview: what the end
	// needs is not negotiable, and the head gets what is left of it.
	tailText, tailCount := truncTail(lines, TruncTailLines)
	headBudget := TruncMaxTokens - EstimateTokens(tailText)
	if headBudget < 0 {
		headBudget = 0
	}
	headLimit := TruncMaxLines
	if rest := len(lines) - tailCount; rest < headLimit {
		headLimit = rest
	}
	headText, headCount := truncHead(lines, headLimit, headBudget)

	omitted := lines[headCount : len(lines)-tailCount]
	errs, warns := truncSignals(omitted)

	var preview strings.Builder
	preview.WriteString(headText)
	if len(omitted) > 0 {
		fmt.Fprintf(&preview, "\n... %d of %d lines omitted (%d mention an error, %d a warning) ...\n\n",
			len(omitted), len(lines), errs, warns)
	}
	preview.WriteString(tailText)

	fmt.Fprintf(&preview,
		"\n[TRUNCATED] Full output saved to: %s\n"+
			"Use read_file with offset/limit to view specific sections.\n", filePath)
	return TruncationResult{
		Content:      preview.String(),
		FullPath:     filePath,
		OmittedLines: len(omitted),
	}
}

// truncHead keeps the first lines that fit the byte and token budgets. The first
// line is always kept, clipped if it alone is too large: an output whose first
// line is enormous must still yield a preview that starts with it.
func truncHead(lines []string, limit, tokenBudget int) (string, int) {
	if limit > len(lines) {
		limit = len(lines)
	}
	if limit <= 0 || tokenBudget <= 0 {
		return "", 0
	}
	byteBudget := tokenBudget * bytesPerToken
	if byteBudget > TruncMaxBytes {
		byteBudget = TruncMaxBytes
	}
	var b strings.Builder
	used := 0
	count := 0
	for i := 0; i < limit; i++ {
		line := lines[i]
		size := len(line) + 1
		if used > 0 && used+size > byteBudget {
			break
		}
		if used == 0 {
			line = clipBytes(line, byteBudget)
			size = len(line) + 1
		}
		b.WriteString(line)
		b.WriteByte('\n')
		used += size
		count++
	}
	return b.String(), count
}

// truncTail keeps the last lines, bounded by the byte cap so one enormous last
// line cannot smuggle the whole output back in.
func truncTail(lines []string, want int) (string, int) {
	if want <= 0 || len(lines) == 0 {
		return "", 0
	}
	if want > len(lines) {
		want = len(lines)
	}
	var kept []string
	used := 0
	for i := len(lines) - 1; i >= 0 && len(kept) < want; i-- {
		size := len(lines[i]) + 1
		if len(kept) > 0 && used+size > TruncMaxBytes {
			break
		}
		line := lines[i]
		if len(kept) == 0 {
			line = clipBytes(line, TruncMaxBytes)
			size = len(line) + 1
		}
		kept = append(kept, line)
		used += size
	}
	for i, j := 0, len(kept)-1; i < j; i, j = i+1, j-1 {
		kept[i], kept[j] = kept[j], kept[i]
	}
	if len(kept) == 0 {
		return "", 0
	}
	return strings.Join(kept, "\n") + "\n", len(kept)
}

// truncSignals counts the omitted lines that mention a failure or a warning. An
// error line is not also counted as a warning.
func truncSignals(lines []string) (errs, warns int) {
	for _, line := range lines {
		if truncErrorSignal.MatchString(line) {
			errs++
			continue
		}
		if truncWarningSignal.MatchString(line) {
			warns++
		}
	}
	return errs, warns
}

// clipBytes cuts a string to at most limit bytes on a rune boundary, so a
// multi-byte character is never split in half.
func clipBytes(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// uniqueToolFile generates a unique file path for saving truncated tool output.
func uniqueToolFile() string {
	os.MkdirAll(truncDir, 0755)
	return filepath.Join(truncDir,
		fmt.Sprintf("tool_%x_%x", time.Now().UnixNano(), rand.Uint64()))
}
