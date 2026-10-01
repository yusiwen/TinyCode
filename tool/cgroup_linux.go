//go:build linux

package tool

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// bashCgroupRoot is where the cgroup v2 hierarchy is mounted. It is a variable
// so the tests can point the discovery logic at a fixture directory.
var bashCgroupRoot = "/sys/fs/cgroup"

// cgSeq keeps the group names unique within one process.
var cgSeq atomic.Int64

// errNoBashCgroup reports that this host cannot host a per-command cgroup: no
// cgroup v2 mount, no writable delegation, or a kernel that does not have the
// files the type needs.
var errNoBashCgroup = errors.New("no writable cgroup v2 delegation")

// newBashCgroup creates a cgroup v2 group for one bash invocation.
//
// It is deliberately conservative: this process's own cgroup is tried first
// (a systemd user session delegates that subtree, which is where a desktop run
// works), then the hierarchy root (a privileged container). Anything else — a
// read-only mount, a root-owned ancestor, cgroup v1 — returns errNoBashCgroup and
// the caller keeps the portable kill path.
//
// It is a variable so a test can force the portable path and record what the tool
// cannot reach there (TestDoubleForkedDescendantEscapesTheTreeWalk).
var newBashCgroup = func() (*bashCgroup, error) {
	if _, err := os.Stat(filepath.Join(bashCgroupRoot, "cgroup.controllers")); err != nil {
		return nil, fmt.Errorf("%w: %s is not a cgroup v2 hierarchy: %v",
			errNoBashCgroup, bashCgroupRoot, err)
	}

	name := fmt.Sprintf("tinycode-bash-%d-%d", os.Getpid(), cgSeq.Add(1))
	candidates := []string{}
	if own := ownCgroupDir(); own != "" {
		candidates = append(candidates, filepath.Join(bashCgroupRoot, own))
	}
	candidates = append(candidates, bashCgroupRoot)

	var lastErr error
	for _, parent := range candidates {
		dir := filepath.Join(parent, name)
		if err := os.Mkdir(dir, 0o755); err == nil {
			return &bashCgroup{dir: dir}, nil
		} else {
			lastErr = err
		}
	}
	return nil, fmt.Errorf("%w: mkdir in %s failed: %v", errNoBashCgroup, bashCgroupRoot, lastErr)
}

// ownCgroupDir returns this process's cgroup v2 path relative to the hierarchy
// root, as "/proc/self/cgroup" reports it ("0::/user.slice/…"), or "" when the
// file cannot be read or does not use the unified layout.
func ownCgroupDir() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(line, "0::")
		if !ok || rest == "" || rest == "/" {
			continue
		}
		// Strip the leading slash so filepath.Join does not discard the root.
		return strings.TrimPrefix(rest, "/")
	}
	return ""
}
