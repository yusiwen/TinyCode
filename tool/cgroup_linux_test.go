//go:build linux

package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNewBashCgroupDiscovery checks the discovery logic without a kernel: a
// fixture root holding cgroup.controllers stands in for the mount, so the group
// is created under it, and a directory without that file is refused.
func TestNewBashCgroupDiscovery(t *testing.T) {
	previous := bashCgroupRoot
	t.Cleanup(func() { bashCgroupRoot = previous })

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	bashCgroupRoot = root

	cg, err := newBashCgroup()
	if err != nil {
		t.Fatalf("newBashCgroup under a fixture root: %v", err)
	}
	if !strings.HasPrefix(cg.dir, root+string(filepath.Separator)) {
		t.Errorf("group dir = %q, want it under %q", cg.dir, root)
	}
	if _, err := os.Stat(cg.dir); err != nil {
		t.Errorf("the group directory was not created: %v", err)
	}
	if err := cg.remove(); err != nil {
		t.Errorf("remove: %v", err)
	}

	// A directory without cgroup.controllers is not a cgroup v2 hierarchy.
	bashCgroupRoot = t.TempDir()
	if _, err := newBashCgroup(); err == nil {
		t.Error("a directory without cgroup.controllers was accepted as a hierarchy")
	}
}

// TestNewBashCgroupRealHierarchy exercises the real mount. It skips with the
// reason where this process has no writable delegation — a container or a CI
// runner — because the tool degrades to the portable kill there by design.
func TestNewBashCgroupRealHierarchy(t *testing.T) {
	cg, err := newBashCgroup()
	if err != nil {
		t.Skipf("no per-command cgroup on this host: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cg.dir, "cgroup.procs")); err != nil {
		t.Errorf("the created group has no cgroup.procs: %v", err)
	}
	if err := cg.remove(); err != nil {
		t.Errorf("remove: %v", err)
	}
}
