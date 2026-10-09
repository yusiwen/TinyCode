# Sandbox

How TinyCode confines what the agent's tools and the commands it runs may touch,
and what that confinement does *not* promise.

The short version: file effects are restricted by policy; the policy is decided
once per run; where the operating system offers a kernel mechanism it enforces
the boundary, and where it does not the host says so instead of pretending.
**[What it does not promise](#what-it-does-not-promise)** is as much a part of
the contract as the rest — read it before relying on any of this.

---

## The shape of it

```
run starts
  └─ policy frozen onto the run's context        types.SandboxPolicy
       ├─ mode        read-only | workspace-write | danger-full-access
       ├─ projectRoot the one root the single-root kernel probe can express
       └─ roots       every directory a write may land under

consumers, each reading only that value
  ├─ path fence        read_file / write_file / edit / apply_patch / lsp tools
  ├─ command boundary  bash (when confinement is on)
  └─ plan-mode guard   bash's string check for writes
```

Nothing re-derives a mode or a root: `tool.PolicyFromConfig` is the one
derivation, and the consumers read the value it produced. That is what stops the
fence and the shell from disagreeing about what is writable — the failure this
design exists to prevent.

## Modes

| Mode | What it means |
| --- | --- |
| `read-only` | No writes, except the sinks a command needs (`/dev/null`). Plan mode maps here. |
| `workspace-write` | Writes under the policy's roots. Build mode maps here. |
| `danger-full-access` | No fence, no launcher. Part of the vocabulary and honoured by both consumers, but **nothing in the product sets it yet**. |

The mode comes from the agent a run is using: `plan` is read-only, everything
else is workspace-write. It is recorded with the policy's `Source` so a refusal
or a report can say where it came from.

## One policy per run

`types.SandboxPolicy` carries the mode, the project root, the writable roots and
their source. `agent.Run` attaches it to the run's context when the run starts,
resolving the roots through `types.ResolveSandboxRoots` — a hook the composition
installs (`tool.InstallSandboxPolicyResolver`), because the agent package cannot
import the tool package.

Two properties follow, and both have tests:

- **Frozen.** A policy is a value. Changing the configuration after a run starts
  does not change what that run may do.
- **Non-widening.** A policy already on the context wins. A nested run (a
  sub-agent) cannot start with a wider boundary than its parent's, and a
  per-call escalation governs only the context it was attached to.

## Writable roots

`tool.WritableRoots()` returns the canonical, symlink-resolved, deduplicated list
the fence and the command boundary both consume: the project root plus the
auto-allowed paths (the working directory).

Two deliberate facts:

- **No temp area is a root.** `/tmp` is not writable through the fence. Adding it
  would widen the boundary, so it is a policy change of its own — with its own
  test — rather than a side effect of anything else.
- **The list may hold several roots; the kernel probe can express one.**
  `openat2(RESOLVE_BENEATH)` is single-root, so the fence runs that probe for the
  project root and decides the remaining roots by comparing resolved paths. A
  mechanism that takes an arbitrary path list (Landlock) is what the command
  boundary uses instead.

## The path fence

Applied by the file tools through `CheckPathAccess(ctx, path)`:

1. **Canonicalize, then compare.** Both the root and the requested path are
   resolved the way the OS resolves them — every component in order, `..` applied
   to the already-resolved prefix — so a symlink, or a `link/..` sequence, cannot
   look contained while pointing outside.
2. **Ask the kernel where it can.** On Linux, `openat2` with
   `RESOLVE_BENEATH | RESOLVE_NO_MAGICLINKS` re-evaluates the path at the moment
   of use and refuses to leave the root: a component swapped for an escaping
   symlink between the check and the open is caught.
3. **Open through the decision.** Where `openat2` cannot cover a path, the
   component walk opens it one component at a time with `O_NOFOLLOW`, so a
   symlink — which a resolved path cannot legitimately contain — is refused with
   `ELOOP` instead of followed. This is the macOS path.
4. **Write atomically.** A replacement write goes into a temp file in the
   target's own directory and is renamed over it, so an interrupted write leaves
   the old bytes rather than a truncated file.

Reads are not restricted. The mode vocabulary claims file effects only.

## The command boundary

Shell commands are guarded in two layers, and only the second is a boundary:

1. **String checks** — `sandbox.deny_commands` and the plan-mode write guard.
   These are speed bumps: `sh -c 'rm -fr /'`, a path assembled from variables, or
   `base64 -d | sh` all walk past a substring match. They stay for the obvious
   cases and for the advice they carry.
2. **The kernel boundary**, when `sandbox.confine_commands` is on. The same
   `['bash','-c',cmd]` argv is wrapped in a launcher and the boundary is applied
   to the process that will run it.

### The launcher

The launcher is **this same binary**, re-exec'd:

```
tinycode __sandbox-exec --mode <mode> --allow <root>… -- <command> [args…]
```

It is intercepted at the very top of `main`, before any flag parsing, because
applying a kernel boundary has to happen after the fork and before the exec of
the command — which no `os/exec` call can express. It gives the exit codes back
to us, which is what the next section needs.

On Linux the launcher applies **Landlock**: an unprivileged, self-imposed
restriction that needs no helper binary and no privileges.

- **Write-class rights only.** Reads stay unrestricted; a command that cannot
  read its own inputs is not confined, it is broken.
- **The mask follows the kernel's ABI.** Handling a right an older kernel does
  not know makes ruleset creation fail outright, so the mask is built from the
  version the kernel reports (`REFER` from ABI 2, `TRUNCATE` from ABI 3).
  `IOCTL_DEV` is deliberately not handled: it governs `ioctl` on devices, not
  file writes.
- **`/dev/null` stays writable in every mode.** A boundary that breaks
  `2>/dev/null` is one nobody can keep switched on.
- Then `prctl(PR_SET_NO_NEW_PRIVS)`, `landlock_restrict_self`, and `exec` — with
  the command resolved through `PATH` first, since `execve` takes a path.

On a platform with no mechanism the launcher **fails** rather than running the
command unconfined.

### The structured channel

A launcher that could not apply the boundary exits **125** *and* writes a
`tinycode-sandbox: ` line to its own stderr. Both are required to classify it, so
a command that exits 125 by itself is not read as a sandbox report — and nothing
here reads the command's stderr: a command that fails because of the boundary
reports its own `EACCES`, which is its business, not the sandbox's.

## Capability facts

The same question — "what is actually enforcing this?" — has two answers, and
they are not the same question:

| Fact | Question | Reported by |
| --- | --- | --- |
| `ContainmentInfo()` | what confines the agent's own file opens? | `openat2`, the component walk, or in-process checks |
| `CommandConfinementAvailable()` | can this host confine a subprocess at all? | Landlock's ABI probe, once per process |

Both are surfaced rather than assumed:

- `/sandbox` prints the containment level, whether command confinement is on and
  available, and the effective writable roots.
- `sandbox.require_hard_boundary` refuses an operation that would be enforced by
  in-process checks alone, including when no project root is configured. It fails
  closed: no approval creates a capability the host lacks.
- A file-mutating tool appends a containment note to its result **only when the
  kernel is not enforcing**, so a degraded host is visible in the result the
  model is reading while ordinary results stay unchanged.

## Persistent grants

"Always allow" outlives the session, so it is recorded where it can be audited:

```json
{
  "sandbox": {
    "allowed_path_grants": [
      { "path": "/somewhere/else", "granted": "2026-10-08T04:00:00Z", "project": "/repo" }
    ]
  }
}
```

- The permission dialog names the file the permanent option writes, so the
  consequence is visible when the choice is made.
- A grant is honoured by every later run: the startup path puts the granted paths on
  the sandbox's allow-list, so the next start writes without a dialog. Only the
  user's own config file is read — a repository-local `./.tinycode/config.json` must
  not be able to grant itself a path outside its own root.
- The older `sandbox.allowed_paths` list is still read and reported, marked as
  recorded before grants carried context. Nothing rewrites it.
- Revocation reaches **both** storage forms — every way a grant can be stored, it
  can be removed.

```bash
tinycode --list-grants             # what has accumulated, with when and from where
tinycode --revoke-grant <path>     # undo one
```

Lifetimes, in one place:

| Answer | Lives | Stored |
| --- | --- | --- |
| Allow once | one call | nowhere |
| Allow session | the process, and the session file for resume | `sessions/<id>.json` |
| Always allow | until revoked | `~/.tinycode/config.json` |

The session allow-list is shared by design: "allow for this session" is a session
fact. What is per-run is the mode and the writable roots.

## Refusals

Every refusal has one shape, defined in `types/refusal.go` (the `next:` line is
wrapped here for width; the tool emits it as a single line):

```
[SANDBOX] path "/etc/passwd": File "/etc/passwd" is outside the project root.
  mode: workspace-write
  ask: interactive
  the narrowest answer that works is: allow "/etc/passwd" once
next: choose the narrowest permission that works — allow once, then allow this
      session, then always allow, which writes the config file
```

The **`ask` field** is the contract, and it is what the agent loop keys off:

| `ask` | Recovery | Terminal? |
| --- | --- | --- |
| `interactive` | a person can grant it, narrowly first | no |
| `unavailable` | nobody can be asked; work inside the roots | no |
| `mode` | another mode would allow it (`/build`) | no |
| `capability` | the machine cannot do it | **yes** |
| `policy` | configuration forbids it (`sandbox.deny_commands`) | **yes** |

A terminal refusal ends the loop instead of spending a model turn on something
nothing the model tries can change. A marker only counts when it **opens** the
result: a command that prints words resembling a refusal is output, not a report.

## Configuration and commands

| Key (`config.json`) | Default | Meaning |
| --- | --- | --- |
| `sandbox.project_root` | the working directory for a real run | the boundary root |
| `sandbox.deny_commands` | a built-in list | substring rules for the command check |
| `sandbox.allowed_paths` | — | legacy persistent grants (read-only now) |
| `sandbox.allowed_path_grants` | — | persistent grants with context |
| `sandbox.require_hard_boundary` | `false` | refuse anything enforced only in process |
| `sandbox.confine_commands` | `false` | run shell commands under the kernel boundary |

| Command | What it does |
| --- | --- |
| `/sandbox` (TUI) | the containment level, the command-boundary switch, the writable roots |
| `--list-grants` | persistent grants, with when and from where |
| `--revoke-grant <path>` | remove one, from either storage form |

### Which layer may set what

The configuration is layered `defaults → ~/.tinycode/config.json → ./.tinycode/config.json`,
and the project layer is **attacker-controlled**: it arrives with the repository
that is being opened, so the agent may run with it before anyone has read it. The
sandbox keys are therefore split by what they can do to the fence.

| Key | A project-local file may… |
| --- | --- |
| `sandbox.project_root` | **not set it** — it could name `/` and remove the boundary |
| `sandbox.allowed_paths` | **not extend it** — it could hand the agent a path nobody allowed |
| `sandbox.allowed_path_grants` | **not add one** — read from the user's file only (#155) |
| `sandbox.deny_commands` | add refusals; the lists accumulate |
| `sandbox.require_hard_boundary` | turn the requirement **on**, never off |
| `sandbox.confine_commands` | turn confinement **on**, never off |

`merge()` has enforced the bottom three since the sandbox landed; the widening
keys are read through the user-layer helpers (`config.UserSandboxBoundary`,
`config.ListAllowedPathGrants`) rather than through the merged configuration, and
`installSandboxBoundary()` in `main.go` is the one place that installs them. A
project-local file asking for one of them is **logged and ignored**
(`project_local_boundary_keys_ignored`), not dropped in silence, so a person who
wrote that key can see it did not take effect.

## How it is tested

| Layer | What it proves | Where |
| --- | --- | --- |
| Unit | policy freezing, non-widening, roots, the launcher argv and exit-code contract, grant records | `go test ./...` |
| Kernel acceptance | a real Landlock boundary: a write **inside** a granted root happens and one **outside** it does not | `TestLandlockBoundaryIsEnforced` (Linux; skips with a reason elsewhere) |
| Wiring | the real binary on a PTY with confinement on, asserting on the **filesystem** afterwards | `tui/testdata/scenarios/confine-bash-write.scenario` |
| Report | `/sandbox`'s output, on screen | `tui/testdata/scenarios/sandbox-command.scenario` |
| Grants | the dialog names the file, the file carries the grant, and a second start writes without a dialog | `tui/testdata/scenarios/permission-allow-always.scenario` |
| Layer rule | a project-local widening key is refused, and the same key in the user's file is allowed | `TestProjectLocalConfigCannotWidenTheFence`, `TestSandboxBoundaryIgnoresTheProjectLayer` |

```bash
go test ./... -count=1 -race             # units and the policy properties
make test-tui-confine                    # the filesystem assertion (needs a kernel mechanism)
go test ./tool/ -run Landlock -v         # which mechanism this host offers
make test-tui-scenarios                  # the rest of the TUI scenarios
```

`make test-tui-confine` probes the **real launcher** and skips with a printed
reason where the host cannot confine a subprocess, rather than passing silently.
Its CI job (`Sandbox confinement (Landlock + PTY)`) prints the kernel's LSM list
and runs the Landlock test with `-v`, because a skipped test prints nothing and a
green job would otherwise read as evidence it does not have.

The acceptance assertion is **selective on purpose**: if the boundary were
unavailable and the command were refused wholesale, *neither* file would exist
and the "inside" half fails. A fail-closed refusal can therefore never read as a
working boundary.

## What it does not promise

- **File effects only.** Network access is unrestricted in every mode. Process
  visibility is unchanged (no namespaces).
- **Confinement is off by default.** `sandbox.confine_commands` is `false`, so the
  default posture for shell commands is still the string checks. That is a
  deliberate choice — a confined command may write only under the roots, and
  toolchains write their own caches — and it is tracked as issue #139 rather than
  left as a comment.
- **macOS cannot confine a subprocess.** The component walk confines the agent's
  own opens; there is no mechanism for a command it spawns, so the launcher
  reports a capability refusal. Command confinement is a Linux feature today.
- **The kernel probe is single-root**, as described above; `danger-full-access` is
  in the vocabulary but nothing sets it; per-call escalation is a tested shape
  with no producer yet (the dialog's ladder is `once → session → always`).

## Where the code lives

| File | Role |
| --- | --- |
| `types/types.go` | `SandboxPolicy`, the mode vocabulary, the context carrier, the roots hook |
| `types/refusal.go` | the refusal vocabulary: marker, ask values, hints, terminal rule |
| `tool/sandboxpolicy.go` | `PolicyFromConfig` — the one derivation — and the resolver hookup |
| `tool/sandbox.go` | the path fence, the session allow-list, the permission queue |
| `tool/sandboxio.go` | reads and atomic writes through the sandbox root |
| `tool/pathbeneath_*.go` | `openat2` containment and the `O_NOFOLLOW` component walk |
| `tool/containment_*.go` | the capability probes and the degraded-result note |
| `tool/sandboxexec.go` | the launcher contract, argv, and classification |
| `tool/confinement_linux.go` | the Landlock ruleset and the launcher entry point |
| `tool/confinement_other.go` | the honest failure where there is no mechanism |
| `config/config.go` | the sandbox keys and the persistent-grant store |
| `tui/sandbox.go` | `/sandbox`, the dialog labels, the always-allow file naming |
