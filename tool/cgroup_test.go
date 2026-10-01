package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestBashCgroupMovesPidsInAndRemovesTheGroup drives the group with an ordinary
// directory standing in for the kernel's: the wiring (which pid is moved in, what
// removal deletes) is the same code the Linux path runs, so it is checked on
// every platform.
func TestBashCgroupMovesPidsInAndRemovesTheGroup(t *testing.T) {
	dir := t.TempDir()
	cg := &bashCgroup{dir: dir}

	if err := cg.add(os.Getpid()); err != nil {
		t.Fatalf("add: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "cgroup.procs"))
	if err != nil {
		t.Fatalf("read cgroup.procs: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != strconv.Itoa(os.Getpid()) {
		t.Errorf("cgroup.procs = %q, want this pid %d", got, os.Getpid())
	}

	// In a real cgroup the kernel owns cgroup.procs and its presence does not
	// block rmdir; in this fixture the file add() created has to go first.
	if err := os.Remove(filepath.Join(dir, "cgroup.procs")); err != nil {
		t.Fatalf("clean the fixture: %v", err)
	}
	if err := cg.remove(); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the group directory survived remove: %v", err)
	}

	// A nil group is inert rather than a panic: the tool calls these methods
	// whenever a cgroup could not be created.
	var none *bashCgroup
	if err := none.add(1); err == nil {
		t.Error("add on a nil group must fail")
	}
	if err := none.remove(); err != nil {
		t.Errorf("remove on a nil group = %v, want nil", err)
	}
}

// TestBashCgroupKillFallsBackToThePIDList covers the kernels older than 5.14:
// without cgroup.kill the group is emptied by signalling every pid in
// cgroup.procs, which is what reaches a double-forked descendant — its parentage
// changed, its cgroup membership did not.
func TestBashCgroupKillFallsBackToThePIDList(t *testing.T) {
	dir := t.TempDir()
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Skipf("cannot start a helper process: %v", err)
	}
	t.Cleanup(func() {
		_ = sleep.Process.Kill()
		_ = sleep.Wait()
	})

	if err := os.WriteFile(filepath.Join(dir, "cgroup.procs"),
		[]byte(strconv.Itoa(sleep.Process.Pid)+"\n"), 0o644); err != nil {
		t.Fatalf("write cgroup.procs: %v", err)
	}

	cg := &bashCgroup{dir: dir}
	if err := cg.kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}

	// Reap it: a killed child that is never waited for stays a zombie, and a
	// zombie still answers signal 0, so processAlive would report a live process
	// for a pid the kernel has already terminated.
	if waitErr := sleep.Wait(); waitErr == nil {
		t.Errorf("the helper exited cleanly, want it killed by the group")
	}
}

// TestBashCgroupKillPrefersCgroupKill pins the fast path: when the kernel
// provides cgroup.kill, that single write is what terminates the group, and the
// pid list is not read at all.
func TestBashCgroupKillPrefersCgroupKill(t *testing.T) {
	dir := t.TempDir()
	killFile := filepath.Join(dir, "cgroup.kill")
	if err := os.WriteFile(killFile, nil, 0o644); err != nil {
		t.Fatalf("create cgroup.kill: %v", err)
	}

	cg := &bashCgroup{dir: dir}
	if err := cg.kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	data, err := os.ReadFile(killFile)
	if err != nil {
		t.Fatalf("read cgroup.kill: %v", err)
	}
	if string(data) != "1" {
		t.Errorf("cgroup.kill holds %q, want 1", data)
	}
	// The pid list was not needed, so it was not created.
	if _, err := os.Stat(filepath.Join(dir, "cgroup.procs")); !os.IsNotExist(err) {
		t.Errorf("the pid list was touched although cgroup.kill was available: %v", err)
	}
}

// TestKillNoteNamesTheMechanism pins the report line: a reader must be able to
// tell whether the timeout kill could have missed a double-forked descendant.
func TestKillNoteNamesTheMechanism(t *testing.T) {
	if got := killNote(30, "cgroup v2 + process group"); !strings.Contains(got, "cgroup v2") || !strings.Contains(got, "30s") {
		t.Errorf("killNote = %q, want the cgroup mechanism and the timeout", got)
	}
	if got := killNote(3, ""); !strings.Contains(got, "process group + tree walk") {
		t.Errorf("killNote without a mechanism = %q, want the portable one named", got)
	}
}
