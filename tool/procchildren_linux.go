//go:build linux

package tool

import (
	"os"
	"strconv"
	"strings"
)

// listProcesses returns every process as a pid/parent-pid pair.
//
// The parent is read from /proc/<pid>/status, whose "PPid:" line needs no
// parsing around parentheses or spaces (unlike /proc/<pid>/stat, where the
// command name can contain both).
func listProcesses() []procEntry {
	dir, err := os.Open("/proc")
	if err != nil {
		return nil
	}
	defer dir.Close()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil
	}

	entries := make([]procEntry, 0, len(names))
	for _, name := range names {
		pid, err := strconv.Atoi(name)
		if err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + name + "/status")
		if err != nil {
			continue // the process exited while we walked the table
		}
		ppid, ok := parsePPid(string(data))
		if !ok {
			continue
		}
		entries = append(entries, procEntry{pid: pid, ppid: ppid})
	}
	return entries
}

// parsePPid extracts the parent pid from the contents of /proc/<pid>/status.
func parsePPid(status string) (int, bool) {
	for _, line := range strings.Split(status, "\n") {
		value, ok := strings.CutPrefix(line, "PPid:")
		if !ok {
			continue
		}
		ppid, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, false
		}
		return ppid, true
	}
	return 0, false
}
