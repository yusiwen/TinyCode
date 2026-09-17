//go:build linux

package tool

import (
	"os"
	"testing"
)

// TestParsePPid covers the parent lookup behind the Linux process walk. The
// status file is read instead of /proc/<pid>/stat because the command name in
// stat can contain both spaces and parentheses ("(my proc)") and would derail a
// field-based parse.
func TestParsePPid(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   int
		ok     bool
	}{
		{"typical", "Name:\tbash\nState:\tS\nPPid:\t1234\nUid:\t1000\n", 1234, true},
		{"first line", "PPid:\t1\n", 1, true},
		{"missing", "Name:\tbash\nState:\tS\n", 0, false},
		{"not a number", "PPid:\tabc\n", 0, false},
		{"empty", "", 0, false},
	}
	for _, tc := range tests {
		got, ok := parsePPid(tc.status)
		if ok != tc.ok || got != tc.want {
			t.Errorf("%s: parsePPid = (%d, %v), want (%d, %v)", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

// TestListProcessesIncludesSelf checks the enumeration returns a usable table on
// a real /proc.
func TestListProcessesIncludesSelf(t *testing.T) {
	self := os.Getpid()
	entries := listProcesses()
	if len(entries) == 0 {
		t.Fatal("listProcesses returned nothing; is /proc readable?")
	}
	for _, entry := range entries {
		if entry.pid != self {
			continue
		}
		if entry.ppid <= 0 {
			t.Errorf("our own entry has ppid %d", entry.ppid)
		}
		return
	}
	t.Fatalf("the process table does not contain our pid %d", self)
}
