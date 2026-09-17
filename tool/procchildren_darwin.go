//go:build darwin

package tool

import "golang.org/x/sys/unix"

// listProcesses returns every process as a pid/parent-pid pair.
//
// It asks the kernel for the process table directly instead of parsing `ps`
// output: no subprocess has to be spawned on the kill path, and the result does
// not depend on ps being installed or permitted in the sandbox the tool runs in.
func listProcesses() []procEntry {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil
	}
	entries := make([]procEntry, 0, len(procs))
	for i := range procs {
		entries = append(entries, procEntry{
			pid:  int(procs[i].Proc.P_pid),
			ppid: int(procs[i].Eproc.Ppid),
		})
	}
	return entries
}
