# Checkpoints and undo

Every file a **file tool** writes is recorded before the write, so the last tool call can be undone.
This page says what that covers, where the record lives, how long it is kept, and — the part that
matters most — what it does *not* cover.

The feature exists because of roadmap entry **C1** (`docs/roadmap.md`), tracked by
[issue #234](https://github.com/yusiwen/TinyCode/issues/234).

## Using it

- In the TUI: `/undo`. It lists the files the last tool call wrote, asks for confirmation, and only
  then restores them. A second `/undo` reaches the call before it.
- Without a TUI: `tinycode --undo` restores the newest call that has not been undone and prints each
  file it restored. With nothing left it prints `Nothing to undo: …` and exits `0`.
- `/sandbox` reports where the store is and how much is in it, together with the boundary below.

Undoing is per tool call, matching the promise "the state before the last tool call". A call that
wrote three files is restored as a unit, and restoring uses the *first* record for each path, so a
call that wrote the same file twice returns to its state before the call rather than to an
intermediate one.

## What is captured

The record is a **pre-image**: the bytes a file held immediately before a tool replaced them, plus
the file's mode. It is written by `writeSandboxed` (`tool/sandboxio.go`) — the one function every
content-changing call site goes through — so no write path can forget to record. A deletion counts
as a content change too: `apply_patch`'s delete operation records the bytes it removes, and undoing
it recreates the file with its original mode.

Two properties are deliberate:

- **Fail closed.** If a pre-image cannot be read for any reason other than "the file does not exist
  yet", the write is **refused** rather than performed unundoably. A caller outside the agent loop —
  a test, a library use — records nothing and behaves exactly as before.
- **The store is not a tool path.** It sits outside `sandbox.project_root` by default, and
  `CheckPathAccess` refuses a tool path beneath it *before* the fence decides. A write to the store
  is answered with a refusal whose `ask` is `reserved`: no permission, once or permanent, grants it.
  `sandbox_full_access` is the one mode that bypasses this, because that mode means no boundary at
  all and fencing one path inside it would make the mode mean less than it says.

## What is NOT captured

**Files changed by a `bash` command or by any other subprocess are not recorded.** Nothing tells
this layer what such a command will touch, so `/undo` cannot return those files to their previous
state. This is a boundary, not a bug: the store accepts a second producer that snapshots the
workspace before a command, and that producer is its own piece of work.

The same applies to anything else that writes without going through the file tools: the headless
browser's own profile writes, `gopls`, and `git` operations run as commands.

## Where the record lives

`checkpoint_dir` in the configuration, defaulting to `~/.tinycode/checkpoints`. Under it, one
directory per project (`TinyCode-<hash of the resolved project root>`), and under that:

```
<project>/calls/<run>-<sequence>-<tool>-step<step>-call<call>.json   the manifest of one tool call
<project>/objects/<first two hex>/<sha256>                           one pre-image, content-addressed
```

A manifest names the call, the session it belongs to when one was known, and one entry per write:
the path, whether the file existed, the pre-image's SHA-256 and the mode. Two calls that share
identical bytes share one object.

A restore marks the call undone in its own manifest (`restored_at`) rather than deleting it, which is
what makes the next `/undo` reach the call before it. The manifest stays as the record that the undo
happened.

## How long it is kept

| Bound | Default | Behaviour |
| :--- | :--- | :--- |
| Calls per project | 50 | The oldest **already-undone** calls are dropped first |
| Store size | 64 MiB | The same order: undone calls are dropped until the store is under it |

**A call that has not been undone is never dropped**, even when that leaves the store over its bound.
Dropping it would destroy the one thing the store exists for, so a project with more live calls than
the limit keeps every one of them. After a prune, objects that no remaining manifest references are
removed; an object shared by a kept call is never collected.

The numbers are not configuration keys. A knob nobody asked for is worse than a documented number,
and the data is one file's previous bytes per call rather than a full tree.

## Checking this page

The counts above are computable, which is the repository's rule for a number written into a document:

```bash
grep -n 'checkpointMaxCalls\|checkpointMaxBytes' tool/checkpoint.go   # the two bounds
grep -rn 'writeSandboxed(' tool/*.go | grep -v _test.go               # the recording call sites
go test ./tool/ -run 'Retention|Checkpoint' -count=1                  # the bounds, asserted
```
