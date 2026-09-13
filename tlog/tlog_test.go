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
