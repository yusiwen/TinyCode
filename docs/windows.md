# Windows

TinyCode builds and runs on Windows. This page states what that means, because
"builds" and "works the same as Linux" are different claims, and the difference is
the boundary.

```console
$ make windows-amd64        # bin/tinycode-windows-amd64.exe
$ make windows-arm64
```

The CI runs both cross-compiles and a `windows-latest` job that builds, vets and
runs the whole test suite on the platform. The tests that need a Unix mechanism or a
property Windows does not model skip themselves through `internal/testenv` with the
reason — the same convention the rest of the suite uses for a gated capability —
so a green job means what it says.

## What works

- The CLI: one-shot runs, `--list-sessions`, `--resume`, `--export-session`,
  `--trajectories`, `--undo`, the grant commands.
- The TUI: Bubble Tea runs on Windows; the frame renderer is pure Go.
- Sessions, trajectories, checkpoints, goals, skills, memory, the MCP client, and
  the LSP client (with a Windows server binary such as `gopls.exe` on `PATH`).
- Every tool, under the same rules as elsewhere — minus the parts below.

Paths are ordinary Windows paths: `~` expands to `%USERPROFILE%`, and the default
session directory is `%USERPROFILE%\.tinycode\sessions`.

## Commands need a POSIX shell

TinyCode runs a command as `bash -c <script>` (or `sh -c` when bash is not on
`PATH`) on every platform, and Windows is no exception. `cmd.exe` is **not** used,
deliberately: its quoting, its operators and its variable syntax are a different
language, the command reader that decides what a command will do
(`tool/shellread.go`) reads POSIX, and the models are told they are driving a
shell. Spawning `cmd.exe` from a tool called `bash` would be a silent lie about
what ran.

So the prerequisite on Windows is a POSIX shell: **[Git for Windows](https://gitforwindows.org/)**
ships one, and it is already installed on most Go development machines. When no
shell is found, the refusal names the dependency instead of returning an `ENOENT`
from inside a tool result:

```
no POSIX shell on PATH: TinyCode runs commands through `bash -c`, not the
platform's command interpreter; install bash (on Windows, Git for Windows ships one)
```

That refusal is what `bash`, `verify.Run` (the test command) and the hook runner
all produce, because all three run a command string.

## The boundary is different, and it says so

Windows has no kernel file boundary for subprocesses that this project can use —
there is no Landlock, no Seatbelt, and no equivalent wired here. The per-platform
default therefore leaves `confine_commands` **off**, and the consequences are the
ones `docs/sandbox.md` describes for any host without a mechanism:

| Mode | On Windows |
| --- | --- |
| Build mode | commands run **unconfined**; the deny list still applies, and file writes made through the *tools* are still fenced in process |
| Plan mode | **refuses to run a command at all**, naming the missing boundary — a read-only mode whose only guard is a string check would be a promise it cannot keep (roadmap H2) |

`/sandbox` prints this rather than leaving it to be inferred.

## What is not supported

- **`cmd.exe` and PowerShell command strings.** The command language is POSIX.
- **Desktop notifications.** There is no notifier for Windows here; the run simply
  finishes without one.
- **The PTY-based visual verification.** The `tuiprobe` module's `pty` package is
  Unix-only, so `tui/verification_test.go` carries `//go:build !windows`. The frame
  and geometry tests still run there; the screenshots and the real-binary PTY smoke
  do not.
- **WSL integration, Windows services, an installer.** The binary is a console
  program you run.
