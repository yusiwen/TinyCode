# Parallel sub-agents: isolation and merge

Roadmap C3. A `task` call runs a sub-agent *inside* the parent run, in the same process. Without isolation,
two parallel tasks writing the same file overwrite each other and the later write wins silently. This page
is what the agent does about that, and what it still does not.

## What isolation does

With `parallel.worktrees` on, a sub-agent that **may write** gets its own git worktree:

```json
{
  "parallel": { "worktrees": true }
}
```

| Step | What happens |
| :--- | :--- |
| Start | `git worktree add --detach <path> HEAD` beside the repository: `<parent>/.<repo>-worktrees/<task label>` |
| Run | the task's sandbox policy names that tree as its root, so its file tools, its `bash` default directory, its relative paths and its command snapshots all resolve there |
| Finish | the tree is staged and diffed against `HEAD`, the diff is applied to the main tree, and the tree is removed |
| Conflict | a path another task in the same run already applied is **refused**, and the task's tree is **kept** so the work is not lost |

The label (`general#7`) is what a person uses to find a kept tree, and what the task's checkpoint manifests
record: a sub-agent's writes are attributable even though its run numbers restart at 1.

A read-only agent (`explore`) gets no worktree — a checkout it cannot use is a cost with no benefit. A
project that is not a git repository falls back to sharing the tree, and the task's result says
`not a git repository` rather than pretending.

**Off by default**, because it changes where a task's work goes; turn it on when you are using parallel
writing tasks deliberately.

## What the result says

| Result | Meaning |
| :--- | :--- |
| `[merged] N file(s) from … into the project:` | the apply succeeded; the tree was removed |
| `[conflict] another task … already changed N file(s)` | another task in this run applied those paths first; this task's version is kept at the printed path |
| `[worktree] the task's work is in … but applying it failed: …` | the main tree moved under the task (or the patch did not apply); the tree is kept for a person to inspect |
| `[worktree] the project is not a git repository …` | no isolation was possible, and the work went into the shared tree |

## What this does not do

- **No automatic merge of content.** The apply is `git apply` of the task's diff. If the main tree has a
  conflicting edit, the apply fails and the tree is kept — it is not merged three-way.
- **A conflict is reported, not resolved.** Two tasks editing the same file means the second one's work
  waits in its tree for a person.
- **No cross-process isolation.** This is one process with several agents.
- **`bash` side effects are only covered by the snapshot.** A command's writes are recorded in the
  worktree's own checkpoint store; the main tree's `/undo` does not know about them. The apply is what
  brings the work across.
- **The language server is process-wide.** A sub-agent's diagnostics are computed against the session's
  root, not its tree, so a task that only exists in its worktree sees no new diagnostics until the merge.
- **Worktrees are not garbage-collected.** A kept tree after a conflict stays until a person removes it
  (`git worktree remove --force <path>`), or the next run's `git worktree prune`.

## Where the code is

| Surface | Code |
| :--- | :--- |
| Creating and removing trees | `tool/worktree.go` — `CreateWorktree`, `RemoveWorktree`, `GitToplevel` |
| Deciding who gets one | `tool/worktree_subagent.go` — `prepareSubagentWorktree`, `mayWrite`, `policyWithRoot` |
| Applying the diff | `tool/worktree_apply.go` — `applyWorktree`, `claimApplied`, `applyNote` |
| The root that follows the call | `tool/sandboxio.go` — `rootFor`, and `tool/sandbox.go` — `resolveAgainstRunRoot` |
| The parent's run number for a background task | `tool/bgtask.go` — `Start(ctx, …)` |
