//go:build !unix

package tool

import (
	"os"
	"os/exec"
)

// configureProcessGroup is a no-op on platforms without process groups.
func configureProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup kills only the child process on platforms without process
// groups.
func killProcessGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}
