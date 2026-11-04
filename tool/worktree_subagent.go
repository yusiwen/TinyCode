package tool

import (
	"context"
	"fmt"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// writingTools are the tools whose presence means a sub-agent may change files.
// An agent that has none of them (the `explore` agent) is read-only, and a
// worktree for it would cost a checkout it cannot use.
var writingTools = []string{"write_file", "edit", "apply_patch"}

// mayWrite reports whether a sub-agent's config allows any file-changing tool.
func mayWrite(cfg *agent.AgentConfig) bool {
	if cfg == nil {
		return false
	}
	for _, name := range writingTools {
		if cfg.IsToolAllowed(name) {
			return true
		}
	}
	return false
}

// prepareSubagentWorktree gives a writing sub-agent its own tree and returns the
// note that goes into the task's result.
//
// It returns "" when isolation is off, when the agent cannot write, or when the
// project is not a git repository — and it says which, because a task that ran in
// the shared tree must not look like one that ran isolated.
func prepareSubagentWorktree(ctx context.Context, deps *TaskToolDeps, cfg *agent.AgentConfig, label string) (string, string) {
	if deps == nil || !deps.Worktrees {
		return "", ""
	}
	if !mayWrite(cfg) {
		return "", ""
	}
	root := DefaultSandbox.projectRootResolved()
	if root == "" {
		return "", "[worktree] no project root is configured, so the task shared the session's tree"
	}
	toplevel := GitToplevel(root)
	if toplevel == "" {
		return "", "[worktree] the project is not a git repository, so the task shared the session's tree"
	}
	path, err := CreateWorktree(toplevel, label)
	if err != nil {
		// A failed checkout is not a reason to fail the task: it runs in the
		// shared tree, and the result says so.
		tlog.Warn("task.worktree", "create_failed", "label", label, "error", err)
		return "", fmt.Sprintf("[worktree] could not create an isolated tree (%v), so the task shared the session's tree", err)
	}
	return path, fmt.Sprintf("[worktree] this task worked in its own tree: %s — not merged into the project yet", path)
}

// policyWithRoot copies the run's policy with the worktree as its root, so the
// sub-agent's fence, opens and commands all agree about where it may write.
func policyWithRoot(ctx context.Context, root string) types.SandboxPolicy {
	policy, ok := types.SandboxPolicyFrom(ctx)
	if !ok {
		policy = PolicyFor(types.SandboxWorkspaceWrite)
	}
	policy.ProjectRoot = root
	// Keep the other writable roots (the session's allowed paths) and add the
	// worktree, which is beside the repository rather than inside it.
	roots := make([]string, 0, len(policy.Roots)+1)
	for _, entry := range policy.Roots {
		if entry != root {
			roots = append(roots, entry)
		}
	}
	policy.Roots = append(roots, root)
	return policy
}
