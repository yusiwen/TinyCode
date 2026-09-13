//go:build unix

package tool

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup starts the command in its own process group so that
// signals sent to the group reach every descendant.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup kills the child and all of its descendants.
//
// Known limit: a descendant that calls setsid(2) (or double-forks into a new
// session) leaves this process group and survives the kill. Closing that hole
// needs cgroups or a PID namespace, which are not portable.
func killProcessGroup(pid int) error {
	// Negative pid targets the whole process group.
	return syscall.Kill(-pid, syscall.SIGKILL)
}
