package agent

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/config"
)

// withTruncation sets the package limits for one test and restores them, with a
// temporary output directory so a test never writes into another's.
func withTruncation(t *testing.T, maxLines, maxBytes, maxTokens, tailLines int) {
	t.Helper()
	lines, bytes, tokens, tail, dir := TruncMaxLines, TruncMaxBytes, TruncMaxTokens, TruncTailLines, truncDir
	TruncMaxLines, TruncMaxBytes, TruncMaxTokens, TruncTailLines = maxLines, maxBytes, maxTokens, tailLines
	truncDir = t.TempDir()
	t.Cleanup(func() {
		TruncMaxLines, TruncMaxBytes, TruncMaxTokens, TruncTailLines, truncDir = lines, bytes, tokens, tail, dir
	})
}

// TestSetTruncationConfig covers the config wiring for truncation limits.
func TestSetTruncationConfig(t *testing.T) {
	savedLines, savedBytes, savedTokens, savedTail, savedDir :=
		TruncMaxLines, TruncMaxBytes, TruncMaxTokens, TruncTailLines, truncDir
	defer func() {
		TruncMaxLines, TruncMaxBytes, TruncMaxTokens, TruncTailLines, truncDir =
			savedLines, savedBytes, savedTokens, savedTail, savedDir
	}()

	SetTruncationConfig(123, 4567, 890, 12, "/tmp/custom-trunc")
	if TruncMaxLines != 123 || TruncMaxBytes != 4567 || TruncMaxTokens != 890 || TruncTailLines != 12 || truncDir != "/tmp/custom-trunc" {
		t.Fatalf("config not applied: lines=%d bytes=%d tokens=%d tail=%d dir=%q",
			TruncMaxLines, TruncMaxBytes, TruncMaxTokens, TruncTailLines, truncDir)
	}

	// Zero/empty values must keep the current setting.
	SetTruncationConfig(0, 0, 0, 0, "")
	if TruncMaxLines != 123 || TruncMaxBytes != 4567 || TruncMaxTokens != 890 || TruncTailLines != 12 || truncDir != "/tmp/custom-trunc" {
		t.Fatalf("zero values changed the settings: lines=%d bytes=%d tokens=%d tail=%d dir=%q",
			TruncMaxLines, TruncMaxBytes, TruncMaxTokens, TruncTailLines, truncDir)
	}
}

// TestTruncateOutputSavedFileIsPrivate checks that the full-output file is
// written 0600: tool output frequently contains secrets.
func TestTruncateOutputSavedFileIsPrivate(t *testing.T) {
	withTruncation(t, 2000, 200*1024, 4000, 20)

	var sb strings.Builder
	for i := 0; i <= TruncMaxLines; i++ {
		sb.WriteString("line\n")
	}
	res := TruncateOutput(sb.String())
	if res.FullPath == "" {
		t.Fatal("expected the full output to be saved")
	}
	info, err := os.Stat(res.FullPath)
	if err != nil {
		t.Fatalf("stat saved output: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("saved output mode = %o, want 600", perm)
	}
	if !strings.Contains(res.Content, "TRUNCATED") {
		t.Errorf("preview is missing the truncation hint: %q", res.Content)
	}
}

// TestTruncateLeavesAnOutputInsideEveryBoundAlone is the control: a preview that
// is under the line, byte and token bounds is returned byte for byte and no file
// is written.
func TestTruncateLeavesAnOutputInsideEveryBoundAlone(t *testing.T) {
	withTruncation(t, 2000, 200*1024, 4000, 20)
	const output = "one\ntwo\nthree\n"
	res := TruncateOutput(output)
	if res.Content != output || res.FullPath != "" || res.OmittedLines != 0 {
		t.Fatalf("a small output was touched: %+v", res)
	}
}

// TestTruncateRespectsTheTokenBudget pins the bound that matters: the preview is
// measured in what the model pays for, not in bytes.
func TestTruncateRespectsTheTokenBudget(t *testing.T) {
	withTruncation(t, 10000, 200*1024, 1000, 20)

	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&sb, "line %04d with a little text\n", i)
	}
	res := TruncateOutput(sb.String())
	if res.FullPath == "" {
		t.Fatal("a 5000-line output was not truncated")
	}
	if res.OmittedLines < 4000 {
		t.Fatalf("omitted %d lines, want most of 5000", res.OmittedLines)
	}
	// The budget covers head and tail; the message and the hint add a little.
	if tokens := EstimateTokens(res.Content); tokens > TruncMaxTokens+150 {
		t.Fatalf("preview is %d tokens, past the %d-token budget", tokens, TruncMaxTokens)
	}
}

// TestTruncateKeepsTheTail pins the half a head-only preview lost: the last line,
// where a failure usually is.
func TestTruncateKeepsTheTail(t *testing.T) {
	withTruncation(t, 10, 200*1024, 4000, 5)

	var sb strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	sb.WriteString("FAIL: the thing that broke\n")
	res := TruncateOutput(sb.String())

	if !strings.Contains(res.Content, "FAIL: the thing that broke") {
		t.Fatalf("the tail is missing from the preview:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "line 0") {
		t.Fatalf("the head is missing from the preview:\n%s", res.Content)
	}
}

// TestTruncateSummarisesWhatItDropped pins the structured part of the preview:
// how many lines went, and how many of them mention a failure or a warning.
func TestTruncateSummarisesWhatItDropped(t *testing.T) {
	withTruncation(t, 10, 200*1024, 4000, 5)

	var sb strings.Builder
	for i := 0; i < 200; i++ {
		switch i {
		case 50, 51, 52:
			sb.WriteString("error: something failed\n")
		case 80, 81:
			sb.WriteString("warning: deprecated call\n")
		default:
			fmt.Fprintf(&sb, "line %d\n", i)
		}
	}
	res := TruncateOutput(sb.String())

	for _, want := range []string{"3 mention an error", "2 a warning", "of 200 lines omitted"} {
		if !strings.Contains(res.Content, want) {
			t.Fatalf("the summary is missing %q:\n%s", want, res.Content)
		}
	}
}

// TestTruncateClipsAnEnormousFirstLine pins the degenerate case: one line larger
// than every bound must still produce a preview that starts with it, not an
// empty one.
func TestTruncateClipsAnEnormousFirstLine(t *testing.T) {
	withTruncation(t, 10, 1000, 4000, 5)
	output := "HEAD-" + strings.Repeat("x", 5000) + "\nsecond\n"
	res := TruncateOutput(output)
	if res.FullPath == "" {
		t.Fatal("an oversized single line was not truncated")
	}
	if !strings.HasPrefix(res.Content, "HEAD-") {
		t.Fatalf("the preview does not start with the line: %q", res.Content[:40])
	}
	if len(res.Content) > 4000 {
		t.Fatalf("the preview is %d bytes, past the byte cap plus the hint", len(res.Content))
	}
}

// TestBytesPerTokenMatchesTheEstimator keeps the loop's byte counting and the
// token estimate in step: bytesPerToken is the estimator's inverse, and a change
// to one without the other would silently move every budget.
func TestBytesPerTokenMatchesTheEstimator(t *testing.T) {
	for _, n := range []int{0, 1, 4, 40} {
		text := strings.Repeat("x", n*bytesPerToken)
		if got := EstimateTokens(text); got != n {
			t.Fatalf("EstimateTokens(%d bytes) = %d, want %d", n*bytesPerToken, got, n)
		}
	}
}

// TestTruncationDefaultsMatchConfig pins the two layers' numbers: the package
// falls back to its own when it is handed zero, and the config defaults are what
// a fresh install starts with.
func TestTruncationDefaultsMatchConfig(t *testing.T) {
	cfg := config.DefaultConfig()
	if cfg.Truncation == nil {
		t.Fatal("the shipped defaults have no truncation section")
	}
	if TruncMaxLines != cfg.Truncation.MaxLines || TruncMaxBytes != cfg.Truncation.MaxBytes {
		t.Fatalf("line/byte defaults = %d/%d, config = %d/%d",
			TruncMaxLines, TruncMaxBytes, cfg.Truncation.MaxLines, cfg.Truncation.MaxBytes)
	}
	if TruncMaxTokens != cfg.Truncation.MaxTokens || TruncTailLines != cfg.Truncation.TailLines {
		t.Fatalf("token/tail defaults = %d/%d, config = %d/%d",
			TruncMaxTokens, TruncTailLines, cfg.Truncation.MaxTokens, cfg.Truncation.TailLines)
	}
}
