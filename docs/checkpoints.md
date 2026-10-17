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

**A `bash` command's writes are captured, but from a snapshot with bounds** (issue #238). Before the
command starts, the project tree is read and hashed; after it, the tree is walked again and only the
paths whose size, modification time or inode moved become entries. A command that changed nothing
writes no manifest at all. The bounds — and what falling outside them means — are:

| Bound | Default | What happens |
| :--- | :--- | :--- |
| Per-file size | 2 MiB | The file is not read. If the command *modifies* it, the change is not recorded, and the tool result says how many such files there were. A file the command *creates* is still recorded even above the bound, because undoing it means removing it. |
| Whole snapshot | 32 MiB / 4000 files | The snapshot is skipped, the command still runs, and the tool result says the changes were not recorded. |
| Location | the project root | A path outside it — an allowed path, a toolchain cache — is not snapshotted: those are not the user's files, and the caches are large by nature. |

Still not captured, by design:

- **Effects outside the filesystem** — a command that pushes, deploys or calls an API.
- **A same-size write that also restores the mtime**, which the size/mtime/inode change detector
  would miss. An ordinary edit moves the mtime; reproducing it is a deliberate act.
- **Symlinks, devices and other non-regular files**, which are counted and reported but not read.

The file tools differ in one respect: their recording is **fail closed**, while a command's is best
effort with the failure surfaced. The reason is what each is for — a file tool's whole purpose is the
write it can refuse, whereas a command's purpose is whatever the model asked for, and a record that
cannot be made is a fact to report rather than a reason to refuse the work.

## How a command's changes are captured

A shell command never says what it will touch, so the only way to have its pre-images is to take them
before it runs. The pre-command tree is read, hashed and stored in the same content-addressed object
store; after the command, one stat walk finds what moved.

**The measured cost**, which is where the bounds come from — this repository, an Apple M4 Pro, with
the 2 MiB per-file cap:

```bash
go test ./tool -run XXX -bench CommandSnapshot -benchtime 5x
# BenchmarkCommandSnapshot-14   5   42853625 ns/op   379.0 files
```

**42.9 ms and 379 files** (2.4 MiB of source; the 2 MiB cap is what removes the five build artefacts
that would otherwise dominate the reading). The 32 MiB / 4000-file bound is about an order of
magnitude above that, so the skip path is for a tree an order of magnitude larger, not for this one.

**Why the bytes are copied and not hard-linked**, which is the cheaper trick: a hard link shares the
inode, so `echo x > file` — a truncate in place, the ordinary way a shell writes a file — changes the
link's content too, and the "pre-image" would be the new bytes. A wrong pre-image is worse than a
missing one, because an undo would silently apply it.

A command's manifest is a normal one: `/undo` reaches it exactly like a file-tool call, it is subject
to the same retention, and a call that can still be undone is still never dropped.

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
