package tool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktrees are how a writing sub-agent gets its own tree (roadmap C3, S2). They
// live beside the repository rather than inside it: a worktree under the project
// root would show up in the session's own `git status` while it runs, and the
// whole point is that the two trees are separate.
//
// The directory is named after the repository and the task label, so a person who
// finds one after a crash can tell where it came from.

// ensureDir creates dir and its parents.
func ensureDir(dir string) error { return os.MkdirAll(dir, 0o755) }

// GitToplevel returns the repository root containing dir, or "" when dir is not
// inside a work tree.
func GitToplevel(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// WorktreePath is where a task's worktree would live: beside the repository, in a
// directory named after it.
func WorktreePath(toplevel, label string) string {
	repo := filepath.Base(toplevel)
	return filepath.Join(filepath.Dir(toplevel), "."+repo+"-worktrees", sanitizeWorktreeName(label))
}

// CreateWorktree checks out the repository's HEAD into a detached worktree for
// one task and returns its path.
//
// Detached on purpose: the sub-agent is not on a branch, so nothing it does can
// move the session's branch, and the apply in S3 is a diff, not a merge.
func CreateWorktree(toplevel, label string) (string, error) {
	path := WorktreePath(toplevel, label)
	if err := ensureDir(filepath.Dir(path)); err != nil {
		return "", err
	}
	out, err := exec.Command("git", "-C", toplevel, "worktree", "add", "--detach", path, "HEAD").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add for %q: %v: %s", label, err, strings.TrimSpace(string(out)))
	}
	return path, nil
}

// RemoveWorktree deletes a worktree and its registration. The caller keeps it
// when an apply conflicted: the work is the thing that must not be lost.
func RemoveWorktree(toplevel, path string) error {
	out, err := exec.Command("git", "-C", toplevel, "worktree", "remove", "--force", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove %s: %v: %s", path, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sanitizeWorktreeName turns a task label into one path component: `general#7`
// becomes `general-7`.
func sanitizeWorktreeName(label string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		}
		return '-'
	}, label)
	name = strings.Trim(name, "-")
	if name == "" {
		return "task"
	}
	return name
}
