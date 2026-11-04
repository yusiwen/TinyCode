package tool

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// WorktreeApply is what merging a task's tree back produced (roadmap C3, S3).
type WorktreeApply struct {
	// Changed is every path the task touched, repository-relative.
	Changed []string
	// Merged is the subset written into the main tree.
	Merged []string
	// Conflict is the subset another task in the same run had already applied.
	// The worktree is kept when this is non-empty: the work is the thing that
	// must not be lost.
	Conflict []string
	// Empty reports a task that changed nothing.
	Empty bool
	// Err is why the apply failed, when it did (a context mismatch against a
	// main tree that moved, for instance).
	Err error
}

// appliedPaths is the per-run record of what has already been merged from a
// task's worktree. Two tasks in one run may write the same file in their own
// trees; the second one's apply is refused rather than silently overwriting the
// first (roadmap C3, S3).
var appliedPaths = struct {
	mu    sync.Mutex
	byRun map[string]map[string]string
}{byRun: map[string]map[string]string{}}

// claimApplied records paths as merged by label and returns those another label
// already merged in the same parent run.
func claimApplied(parentRun string, label string, paths []string) []string {
	appliedPaths.mu.Lock()
	defer appliedPaths.mu.Unlock()
	if appliedPaths.byRun == nil {
		appliedPaths.byRun = map[string]map[string]string{}
	}
	seen := appliedPaths.byRun[parentRun]
	if seen == nil {
		seen = map[string]string{}
		appliedPaths.byRun[parentRun] = seen
	}
	var conflicts []string
	for _, path := range paths {
		if other, ok := seen[path]; ok && other != label {
			conflicts = append(conflicts, path)
			continue
		}
		seen[path] = label
	}
	return conflicts
}

// applyWorktree stages the task's tree, diffs it against its base, and applies
// that diff to the main tree.
//
// Staging first is what makes new files part of the diff: `git diff HEAD` alone
// omits the files a task created, which is most of what a writing task does.
func applyWorktree(toplevel, worktree, label string, parentRun string) WorktreeApply {
	run := func(args ...string) (string, error) {
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return strings.TrimSpace(string(out)), nil
	}

	if _, err := run("-C", worktree, "add", "-A"); err != nil {
		return WorktreeApply{Err: err}
	}
	names, err := run("-C", worktree, "diff", "--cached", "--name-only", "HEAD")
	if err != nil {
		return WorktreeApply{Err: err}
	}
	if names == "" {
		return WorktreeApply{Empty: true}
	}
	paths := strings.Split(names, "\n")

	if conflicts := claimApplied(parentRun, label, paths); len(conflicts) > 0 {
		return WorktreeApply{Changed: paths, Conflict: conflicts}
	}

	// The patch is applied with git rather than copied file by file: it carries
	// deletions, mode changes and binary content, and it fails loudly instead of
	// half-writing a tree that moved underneath the task.
	patchCmd := exec.Command("git", "-C", worktree, "diff", "--cached", "--binary", "HEAD")
	patch, err := patchCmd.Output()
	if err != nil {
		return WorktreeApply{Changed: paths, Err: fmt.Errorf("git diff --cached: %w", err)}
	}
	apply := exec.Command("git", "-C", toplevel, "apply", "--binary", "--whitespace=nowarn", "-")
	apply.Stdin = bytes.NewReader(patch)
	if out, err := apply.CombinedOutput(); err != nil {
		return WorktreeApply{Changed: paths, Err: fmt.Errorf("git apply: %v: %s", err, strings.TrimSpace(string(out)))}
	}
	return WorktreeApply{Changed: paths, Merged: paths}
}

// applyNote renders the apply outcome for the task's result.
func applyNote(worktree string, apply WorktreeApply) string {
	switch {
	case apply.Err != nil:
		return fmt.Sprintf("[worktree] the task's work is in %s but applying it to the project failed: %v\nThe tree is kept; inspect it and apply it by hand.",
			worktree, apply.Err)
	case len(apply.Conflict) > 0:
		var b strings.Builder
		fmt.Fprintf(&b, "[conflict] another task in this run already changed %d file(s) this task also wrote:\n", len(apply.Conflict))
		for _, path := range apply.Conflict {
			fmt.Fprintf(&b, "  %s\n", path)
		}
		fmt.Fprintf(&b, "This task's version is kept at %s and was not applied.", worktree)
		return b.String()
	case apply.Empty:
		return fmt.Sprintf("[worktree] the task changed nothing; %s was removed", worktree)
	default:
		return fmt.Sprintf("[merged] %d file(s) from %s into the project:\n  %s", len(apply.Merged), worktree, strings.Join(apply.Merged, "\n  "))
	}
}
