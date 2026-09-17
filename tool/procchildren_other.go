//go:build unix && !darwin && !linux

package tool

import (
	"os/exec"
	"strconv"
	"strings"
)

// listProcesses returns every process as a pid/parent-pid pair.
//
// Other unix platforms get the portable fallback: `ps -Ao pid=,ppid=`. When ps
// is missing or refuses to run the list is empty, and killProcessGroup falls
// back to the process group alone.
func listProcesses() []procEntry {
	out, err := exec.Command("ps", "-Ao", "pid=,ppid=").Output()
	if err != nil {
		return nil
	}
	var entries []procEntry
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, errPid := strconv.Atoi(fields[0])
		ppid, errPpid := strconv.Atoi(fields[1])
		if errPid != nil || errPpid != nil {
			continue
		}
		entries = append(entries, procEntry{pid: pid, ppid: ppid})
	}
	return entries
}
