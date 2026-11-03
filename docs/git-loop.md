# The git loop: changes, commits, pushes, pull requests

Roadmap C2. After a run the agent tells you what it changed; four commands turn that into a commit and,
if you want, a pushed branch and a pull request. Nothing here happens on its own: **the preview is
automatic, every action is confirmed.**

## `/changes` — what the run changed

```
3 file(s) changed, +42/-7 — 2 written by this run's tool calls, 1 other.
  M   agent/agent.go             +30/-2   this run
  M   docs/roadmap.md            +12/-5   other
  ??  notes.txt                  +3/-0    this run
A shell command can write files the checkpoint does not see; `git status` is the full truth.
```

Two sources, and the difference matters:

- **The working tree** (`git status`, `git diff --numstat`) is the full set of changes. It is always
  complete.
- **The checkpoint store** (roadmap C1) records which files a *write tool* touched, per call, with the
  session and a timestamp. That is what labels a file "this run".

A `bash` command can write anything, and its writes leave no checkpoint, so they appear as "other". The
report never claims otherwise: it describes the tree, and labels what it can explain.

The one line after a run (`This run's tool calls changed N file(s), +A/-R — /changes …`) appears only
when the run itself wrote something. It is a line, not a dialog, because the idle continuation driver
(roadmap D7) can finish a round with nobody watching.

## `/commit <message>` — the run's files, and only those

```
/commit feat: read the config once
```

`/commit` stages the files **this run's tool calls wrote** and commits them. Your unrelated edits stay
uncommitted — that is the point of scoping, and it is what makes the loop safe to use in a repository you
are already working in.

`/commit --all <message>` stages the whole working tree, like `git add -A`, and says so in the prompt.

Both forms print the paths that will be staged and ask once. `/commit` without a message prints usage and
stages nothing.

If git refuses — a failing hook, a signing key that wants a passphrase, an empty index — the error is
shown as git reported it. Nothing is downgraded to an unsigned commit silently.

## `/push` — the current branch, never the default one

`/push` pushes the current branch to `origin` with `-u`, so the next push and `/pr` know where it goes.
It refuses:

| Situation | What you see |
| :--- | :--- |
| Current branch is the default branch | `Push refused: <branch> is the default branch. Create a branch first` |
| Detached HEAD | `HEAD is detached: check out a branch before pushing` |
| No `origin` | `no origin remote to push to` |
| Authentication failed | the failure is named *and* git's own text is kept |
| Remote moved on | `/push --force` uses `--force-with-lease`, and refuses when the local ref is stale |

`--force` never unlocks the default branch: the refusal is in the tool, so a flag cannot bypass it.

## `/pr <title>` — a pull request, or the URL to open one

```
/pr feat: the git loop
```

The dialog names the branch and the base before anything happens. With `gh` (GitHub CLI) installed,
`gh pr create --base <default> --head <branch> --title <title> --body <body>` runs; the body is a short
note plus the last ten commit subjects unless you pass one. Without `gh`, **nothing runs** and you get the
compare URL:

```
https://github.com/<owner>/<repo>/compare/<base>...<branch>?expand=1
```

It refuses the default branch, a branch with no upstream (`push first`), and a branch with commits the
remote does not have — all three would produce a pull request that is empty or wrong.

## What is deliberately not here

- **Push and pull request are user commands, not tools.** The model can prepare the branch and the commit;
  it cannot spend your credentials or publish a branch. The git tools it has remain read-only plus
  `git_commit`, whose `paths` argument scopes the stage.
- **No automatic commit.** "Automatic" is the preview. A commit is one confirmation away, always.
- **No semantic review.** The report states what changed; whether the change is right is your call.
- **TUI only.** `/push` and `/pr` do not exist in one-shot mode, where there is no dialog to confirm them.

## Where the code is

| Surface | Code |
| :--- | :--- |
| The report | `tool.RunChanges` in `tool/changes.go`; `noteRunChanges` and the `/changes` arm in `tui/update.go` |
| The commit | `tool.CommitChanges` in `tool/git.go`; `handleCommit` |
| The push | `tool.PushBranch` in `tool/git_remote.go`; `handlePush` |
| The pull request | `tool.OpenPullRequest` in `tool/git_pr.go`; `handlePullRequest` |
