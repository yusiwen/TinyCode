//go:build unix

package tool

import (
	"os"
	"os/exec"
	"syscall"
)

// procEntry is one process in the parent/child tree.
type procEntry struct {
	pid  int
	ppid int
}

// configureProcessGroup starts the command in its own process group so that
// signals sent to the group reach every descendant.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the child, every descendant found in the process tree,
// and finally the whole process group.
//
// The group alone is not enough: a descendant that calls setsid(2) leaves the
// group and would survive. setsid does not change parentage, so such a process
// is still reachable through the tree — but only while its ancestors are alive,
// because killing the shell re-parents the survivors to init. The tree is
// therefore collected first and killed by pid before the group signal.
//
// Known limit: a descendant that double-forks (its immediate parent exits while
// the shell keeps running) re-parents to init before the tree walk can see it.
// Closing that hole needs cgroups or a PID namespace, which are not portable.
func killProcessGroup(pid int) error {
	for _, dpid := range descendantPIDs(pid) {
		// Best effort: a process may have exited between the walk and the kill.
		_ = syscall.Kill(dpid, syscall.SIGKILL)
	}
	// Negative pid targets the whole process group.
	return syscall.Kill(-pid, syscall.SIGKILL)
}

// descendantPIDs returns every process below root in the parent/child tree,
// deepest entries included. The process table comes from listProcesses, which
// each platform implements without shelling out where it can.
func descendantPIDs(root int) []int {
	if root <= 1 {
		return nil
	}

	children := make(map[int][]int)
	for _, entry := range listProcesses() {
		if entry.pid <= 1 || entry.ppid <= 0 {
			continue
		}
		children[entry.ppid] = append(children[entry.ppid], entry.pid)
	}

	self := os.Getpid()
	var (
		found   []int
		visited = map[int]bool{root: true}
		queue   = []int{root}
	)
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		for _, child := range children[current] {
			// Never signal ourselves, and never walk a node twice: a malformed
			// table must not turn this into an infinite loop.
			if child <= 1 || child == self || visited[child] {
				continue
			}
			visited[child] = true
			found = append(found, child)
			queue = append(queue, child)
		}
	}
	return found
}
