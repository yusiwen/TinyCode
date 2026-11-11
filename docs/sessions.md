# Sessions and branches

A session is one JSON file under the session directory (`~/.tinycode/sessions` by default, `--session-dir`
to point elsewhere). It holds the transcript, the model and provider route, the task list, the goal and
the branch link; writes go through a temporary file and a rename, so a crash cannot leave half a
transcript behind, and the file is created `0600`.

The CLI can resume (`--resume`), list (`--list-sessions`), search (`--search-sessions`), export
(`--export-session`) and delete (`--delete-session`) a session. The TUI owns the interactive side:
`/session` switches, `/fork` branches and `/compare` relates two branches. This page documents the
branch view and the comparison; the file format is the `session.Session` struct.

## Branches

`/fork [label]` copies the current conversation up to its newest message into a new session and
switches to it. The new session records two fields the rest of this page is built on:

- `parent_session` — the id it forked from. This is the authoritative link.
- `fork_at` — the message count at the fork.

The branch id is derived from the parent (`<parent>-<label>`, or `<parent>-branch-<n>` without a
label), which is convenient to read but is **not** the link: a renamed file or a hand-written session
carries the same fields without following the naming rule. The task list and the goal travel with the
branch, and the goal arrives disarmed, because activation is process-local and no file ever carries it.

## The tree

`--list-sessions` and the TUI's `/session` draw the store as a forest, a branch indented under the
session it forked from:

```text
Available sessions:
  TUI-20260101-000000          write the parser     12 msgs  2026-01-01 00:00
  └─ TUI-20260101-000000-fast  write the parser      9 msgs  2026-01-01 00:10  [forked at 7]
```

- Roots are ordered least recently updated first, the same order the flat list used, so the newest
  session stays closest to the prompt. Siblings are ordered by creation time.
- A session whose `parent_session` names a file that is not in the store — or itself — is drawn at the
  top level and marked as an orphan. Deleting a parent does not delete its branches.
- A parent cycle can only be produced by editing files by hand (a fork's id is always longer than its
  parent's). One member of the cycle — the smallest id, so the choice is repeatable — drops its link
  and is drawn as a root marked as the broken end. Every member stays visible; the view never drops a
  session because its history is inconsistent.
- A file that cannot be parsed is skipped by the listing and the tree alike, exactly as the flat list
  skipped it. `--resume` on that id reports the real error.

The tree reads every session file in the directory, so it costs one `Load` per session. That is the
same cost the flat list already paid, and the forest is computed from those reads rather than from a
second pass.

## Comparing two branches

`/compare <a> <b>` answers two questions that are deliberately independent:

- **Where do the branch chains meet?** The nearest session that is an ancestor of both, whether one is
  an ancestor of the other, and the fork point recorded on each side below that ancestor.
- **Where do the transcripts stop agreeing?** The number of identical leading messages, and the first
  message that differs on each side.

The second question is not answered from the link: a `/drop`, a compaction or an edited copy can leave
the transcripts agreeing for fewer messages than `fork_at` records, and reporting both is what makes
that visible instead of letting the link paper over it. Messages are compared on what the model is
sent — role, name, tool-call identity and content. Reasoning is not compared because it is not resent.

A session compared with itself is refused, as is an id that is not in the store; both print one line
and leave the transcript untouched.

## What this does not do

- **No merge.** A branch is a separate conversation; the tree shows the relationship, not a way to
  combine two.
- **No rename or re-parent.** The link is whatever the file records; edit the file to change it.
- **No remote sessions.** The forest is one directory on one machine.
