package agent

import (
	"os"
	"strings"
	"testing"
)

// TestSetTruncationConfig covers the config wiring for truncation limits.
func TestSetTruncationConfig(t *testing.T) {
	savedLines, savedBytes, savedDir := TruncMaxLines, TruncMaxBytes, truncDir
	defer func() {
		TruncMaxLines, TruncMaxBytes, truncDir = savedLines, savedBytes, savedDir
	}()

	SetTruncationConfig(123, 4567, "/tmp/custom-trunc")
	if TruncMaxLines != 123 || TruncMaxBytes != 4567 || truncDir != "/tmp/custom-trunc" {
		t.Fatalf("config not applied: lines=%d bytes=%d dir=%q", TruncMaxLines, TruncMaxBytes, truncDir)
	}

	// Zero/empty values must keep the current setting.
	SetTruncationConfig(0, 0, "")
	if TruncMaxLines != 123 || TruncMaxBytes != 4567 || truncDir != "/tmp/custom-trunc" {
		t.Fatalf("zero values changed the settings: lines=%d bytes=%d dir=%q", TruncMaxLines, TruncMaxBytes, truncDir)
	}
}

// TestTruncateOutputSavedFileIsPrivate checks that the full-output file is
// written 0600: tool output frequently contains secrets.
func TestTruncateOutputSavedFileIsPrivate(t *testing.T) {
	savedDir := truncDir
	truncDir = t.TempDir()
	defer func() { truncDir = savedDir }()

	// One line past the line limit forces a dump.
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
	// The preview must still tell the model where the rest is.
	if !strings.Contains(res.Content, "TRUNCATED") {
		t.Errorf("preview is missing the truncation hint: %q", res.Content)
	}
}
