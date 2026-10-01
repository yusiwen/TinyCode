package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// bashCgroup is one cgroup v2 group holding a single bash invocation and every
// process that descends from it.
//
// It exists because the process group and the process-tree walk cannot reach a
// descendant that double-forks: the intermediate parent exits, the survivor
// re-parents to init and leaves the group, so both the tree and the group signal
// miss it. Cgroup membership is inherited and cannot be given up, so the
// survivor is still inside the group this type represents (issue #9).
//
// The type is portable so the wiring is the same everywhere; only the
// constructor is platform-specific (newBashCgroup), and a platform without
// cgroups returns an error, which leaves the portable kill path in charge.
type bashCgroup struct {
	dir string
}

// add moves pid into the group. It must be called right after the command
// starts: a process that was already forked elsewhere is not moved by moving its
// parent, so the window is the moment between Start and this call.
func (c *bashCgroup) add(pid int) error {
	if c == nil || c.dir == "" {
		return fmt.Errorf("no cgroup")
	}
	path := filepath.Join(c.dir, "cgroup.procs")
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0o644); err != nil {
		return fmt.Errorf("move pid %d into %s: %w", pid, c.dir, err)
	}
	return nil
}

// kill terminates every process in the group.
//
// cgroup.kill (Linux 5.14+) does it in one kernel operation and cannot race with
// a process forking while the kill runs. Where the file does not exist, every pid
// in cgroup.procs is signalled instead — that still reaches a double-forked
// descendant, because it is the *cgroup* membership, not the parentage, that puts
// it in the list.
func (c *bashCgroup) kill() error {
	if c == nil || c.dir == "" {
		return fmt.Errorf("no cgroup")
	}
	// Stat first, never O_CREATE: cgroupfs has no cgroup.kill on a kernel older
	// than 5.14, and writing a new file into a cgroup directory is not something
	// that filesystem allows (the errno differs by kernel), so a failed create
	// must not be mistaken for a failed kill.
	killPath := filepath.Join(c.dir, "cgroup.kill")
	if _, statErr := os.Stat(killPath); statErr == nil {
		if err := os.WriteFile(killPath, []byte("1"), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", killPath, err)
		}
		return nil
	}

	data, err := os.ReadFile(filepath.Join(c.dir, "cgroup.procs"))
	if err != nil {
		return fmt.Errorf("read %s: %w", filepath.Join(c.dir, "cgroup.procs"), err)
	}
	var firstErr error
	for _, field := range strings.Fields(string(data)) {
		pid, convErr := strconv.Atoi(field)
		if convErr != nil || pid <= 1 || pid == os.Getpid() {
			continue
		}
		proc, findErr := os.FindProcess(pid)
		if findErr != nil {
			continue
		}
		// On unix FindProcess always succeeds and Kill sends SIGKILL; the lookup
		// is what makes this portable to a platform whose cgroup support would
		// return the same wording.
		if killErr := proc.Kill(); killErr != nil && firstErr == nil {
			firstErr = fmt.Errorf("kill pid %d: %w", pid, killErr)
		}
	}
	return firstErr
}

// remove deletes the group directory. It is best effort: a command that
// deliberately left a daemon behind keeps it in the group, and the directory then
// stays until that process exits (the kernel refuses to remove a populated
// cgroup).
func (c *bashCgroup) remove() error {
	if c == nil || c.dir == "" {
		return nil
	}
	return os.Remove(c.dir)
}

// killNote is the line appended to a timed-out command's output, naming what
// actually killed it. The mechanism matters to a reader: "process group + tree
// walk" means a double-forked descendant may have survived.
func killNote(timeout int, mechanism string) string {
	if mechanism == "" {
		mechanism = "process group + tree walk"
	}
	return fmt.Sprintf("[tool: timed out after %ds; killed with %s]", timeout, mechanism)
}
