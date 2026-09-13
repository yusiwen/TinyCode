package agent

import "testing"

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
