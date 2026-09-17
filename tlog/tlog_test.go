package tlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readOnlyFile returns the single log file in dir.
func readOnlyFile(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 log file in %s, got %d", dir, len(entries))
	}
	return filepath.Join(dir, entries[0].Name())
}

// TestInitWritesPrivateSingleLineEntries covers the log-file permissions and
// the escaping that keeps one log call on one line.
func TestInitWritesPrivateSingleLineEntries(t *testing.T) {
	restore := defaultLogger.file
	defer func() {
		defaultLogger.mu.Lock()
		if defaultLogger.file != nil {
			defaultLogger.file.Close()
		}
		defaultLogger.file = restore
		defaultLogger.mu.Unlock()
	}()

	dir := t.TempDir()
	Init(dir, LevelTrace)

	Info("svc", "one line", "detail", "value\nFORGED line\r\n")
	Flush()

	path := readOnlyFile(t, dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("log file mode = %o, want 600", perm)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 log line, got %d:\n%s", len(lines), data)
	}
	if strings.Contains(string(data), "FORGED") && !strings.Contains(string(data), `\nFORGED`) {
		t.Errorf("newline in a value was not escaped: %q", data)
	}
}

// TestInitReleasesPreviousHandle checks that re-initializing switches the sink
// to the new directory instead of leaking the old handle.
func TestInitReleasesPreviousHandle(t *testing.T) {
	defer func() {
		defaultLogger.mu.Lock()
		if defaultLogger.file != nil {
			defaultLogger.file.Close()
			defaultLogger.file = nil
		}
		defaultLogger.mu.Unlock()
	}()

	first := t.TempDir()
	Init(first, LevelTrace)
	Info("svc", "first entry")
	Flush()

	second := t.TempDir()
	Init(second, LevelTrace)
	Info("svc", "second entry")
	Flush()

	data, err := os.ReadFile(readOnlyFile(t, second))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "second entry") {
		t.Errorf("new log file missing the entry written after re-init: %q", data)
	}
	if strings.Contains(string(data), "first entry") {
		t.Errorf("re-init did not switch sinks: %q", data)
	}
}

// TestLevelFiltering checks the level gate still works after the rework.
func TestLevelFiltering(t *testing.T) {
	defer func() {
		defaultLogger.mu.Lock()
		if defaultLogger.file != nil {
			defaultLogger.file.Close()
			defaultLogger.file = nil
		}
		defaultLogger.mu.Unlock()
	}()

	dir := t.TempDir()
	Init(dir, LevelWarn)

	Debug("svc", "debug entry")
	Info("svc", "info entry")
	Warn("svc", "warn entry")
	Flush()

	data, err := os.ReadFile(readOnlyFile(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "debug entry") || strings.Contains(s, "info entry") {
		t.Errorf("below-level entries were written: %q", s)
	}
	if !strings.Contains(s, "warn entry") {
		t.Errorf("warn entry missing: %q", s)
	}
}

// TestParseLevel maps level names case-insensitively and falls back to INFO for
// anything unknown, so a typo in the config cannot silence the log.
func TestParseLevel(t *testing.T) {
	tests := []struct {
		in   string
		want Level
	}{
		{"trace", LevelTrace},
		{"TRACE", LevelTrace},
		{"Trace", LevelTrace},
		{"debug", LevelDebug},
		{"info", LevelInfo},
		{"warn", LevelWarn},
		{"error", LevelError},
		{"", LevelInfo},
		{"verbose", LevelInfo},
		{"warning", LevelInfo},
	}
	for _, tc := range tests {
		if got := ParseLevel(tc.in); got != tc.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestSetLevelFiltersOutput checks that SetLevel applies immediately and that
// entries below the threshold are dropped rather than written.
func TestSetLevelFiltersOutput(t *testing.T) {
	restore := defaultLogger.file
	defer func() {
		defaultLogger.mu.Lock()
		if defaultLogger.file != nil {
			defaultLogger.file.Close()
		}
		defaultLogger.file = restore
		defaultLogger.mu.Unlock()
	}()

	dir := t.TempDir()
	Init(dir, LevelError)
	SetLevel(LevelWarn)

	Warn("svc", "kept-warning")
	Info("svc", "dropped-info")
	Trace("svc", "dropped-trace")
	Error("svc", "kept-error")
	Flush()

	data, err := os.ReadFile(readOnlyFile(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "kept-warning") || !strings.Contains(content, "kept-error") {
		t.Errorf("entries at or above the level were dropped:\n%s", content)
	}
	if strings.Contains(content, "dropped-info") || strings.Contains(content, "dropped-trace") {
		t.Errorf("entries below the level were written:\n%s", content)
	}
	if !strings.Contains(content, "WARN") || !strings.Contains(content, "ERROR") {
		t.Errorf("level names are missing:\n%s", content)
	}
	if strings.Contains(content, "INFO") {
		t.Errorf("a filtered level still appeared:\n%s", content)
	}

	SetLevel(LevelTrace)
	if defaultLogger.level != LevelTrace {
		t.Errorf("SetLevel(Trace) left the level at %v", defaultLogger.level)
	}
}
