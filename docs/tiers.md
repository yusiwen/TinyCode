# Isolation tiers: what a command runs under

Roadmap C6. TinyCode can confine what the agent does in two different ways, and they answer different
questions:

- the **mode** (`read-only`, `workspace-write`, `danger-full-access`) says what a *write* may land on;
- the **tier** says what a *command* runs under — the shell, its children, and everything they can reach.

This page is about the second one. [sandbox.md](sandbox.md) is the first.

## The tiers

| Tier | Covers | Read | Write | Network | Needs |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `none` | nothing | all | all | all | — |
| `kernel` (default) | file writes and command writes | all | the policy's roots | all | Landlock (Linux) or Seatbelt (macOS) |
| `container` | **commands**, inside an image | the image plus explicit mounts | the project mount (read-only for a read-only run) | `none` unless configured | a Docker or Podman daemon and an image |

The kernel tier is the operating system's own mechanism and it bounds writes only: a command can still read
what the agent's user can read, and reach the network. The container tier is the first one that takes both
away.

## Choosing one

```json
{
  "sandbox": {
    "tier": "container",
    "container": { "runtime": "docker", "image": "golang:1.27", "network": "none", "memory": "2g", "pids": 512 }
  }
}
```

| `tier` | Meaning |
| :--- | :--- |
| `kernel` (default) | the kernel boundary, as it has always been |
| `container` | commands run in the image; **a host without a runtime refuses the command** rather than falling back |
| `auto` | container when the host has a runtime, kernel otherwise |
| `none` | no boundary for commands (the file fence still applies unless the mode says otherwise) |

**Fail-closed is the rule for a named tier.** `container` is a promise, so a host that cannot keep it says so
and the command does not run. `auto` is the only value that falls back, because it is asking a question
rather than making one.

## Read-only agents narrow by themselves

A delegated agent that cannot write (`explore`) runs on the container tier with a read-only project mount
when the host has a runtime and an image, even if the session is on `kernel`. That is a **narrowing**: it
takes away the network and the host's filesystem and grants nothing the agent could not do before. A host
without a runtime keeps the session's tier for it — an opportunistic narrowing must not fail a task that
could have run.

`/sandbox` reports both: the tier the session uses, and what a read-only agent would use.

## What it costs

- **A cold cache per command.** The container's scratch and toolchain caches live in a `tmpfs`, so they do
  not survive the command. A `go build` in the container pays full price every time. Mounting the host's
  cache is possible but trades a boundary for speed; it is not the default.
- **Startup latency.** Every command pays the runtime's container start.
- **Image size.** A useful image (a toolchain) is hundreds of megabytes; the tier does not shrink it.

## What it does not promise

- **The container's security is the image's.** A command runs with whatever the image contains, and an
  image you did not build may contain anything. The image is never chosen for you and never pulled
  implicitly (`--pull never`): a missing image is an error.
- **No T3.** There is no remote or VM tier yet.
- **The file tools stay in the host process.** T2 is about commands; the agent's own `read_file`/`write_file`
  calls remain fenced by the kernel tier.
- **No per-command tier switching from the model.** The tier is part of the run's frozen policy; a model
  cannot ask for a weaker or stronger one.
- **`--tmpfs /tmp:rw,exec` is an assumption**: images that need a writable path elsewhere (or noexec `/tmp`)
  fail with their own error rather than being accommodated.

## Where the code is

| Surface | Code |
| :--- | :--- |
| Probe, tier resolution, argv | `tool/container.go` — `ProbeContainer`, `ResolveTier`, `ContainerInvocation`, `ReadOnlyTier` |
| The policy field | `types.SandboxPolicy.Tier`, set by `tool.PolicyFromConfig` |
| The spawn | `tool/sandboxexec.go` — `bashInvocation` |
| The report | `/sandbox` in `tui/sandbox.go` |
| Config | `sandbox.tier`, `sandbox.container` |
