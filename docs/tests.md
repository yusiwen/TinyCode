# Tests as feedback

Roadmap C5. The agent can run the project's test suite and read the result, so a change is followed by
"does it still pass" instead of being followed by hope. Compile-level diagnostics were already fed back
through the language server; this is the missing half.

## What runs

The command is **the project's own**. It is discovered once per run, in this order:

| Marker | Command |
| :--- | :--- |
| `test.command` in the config | that command, verbatim (wins over everything) |
| `go.mod` | `go test ./...` |
| `package.json` with a `test` script | `npm test --silent` |
| `Makefile` with a `test:` target | `make test` |
| none of the above | a report naming `test.command` — not a silent success |

`package.json` without a `test` script counts as "none": npm would run its placeholder message and exit 0,
which looks like a passing suite and is not one.

## The three surfaces

**The model asks.** `run_tests` runs the discovered command and returns a report: pass or fail, the exit
code, the duration, and the **tail** of the output (the default cap is 4000 bytes, so a failing suite cannot
flood the context). It is registered always; discovery is what makes it work without configuration.

**The loop runs it after a write** — only when you ask:

```json
{
  "test": {
    "command": "make check",
    "timeout_ms": 120000,
    "max_output_bytes": 4000,
    "auto_after_write": true
  }
}
```

With `auto_after_write` on, a successful `write_file`, `edit` or `apply_patch` is followed by the suite, and
its summary is appended to that tool's result:

```
wrote internal/config/config.go (1 file, 24 lines)
[tests] tests failed (exit 1) in 1.2s (go test ./...)
--- FAIL: TestLoadHooks (0.00s)
    hooks_test.go:37: a project-local hooks section was not reported
```

The model reads the write and its verification together, which is the feedback loop the entry is about.
`bash` is deliberately **not** in the set: a shell command's effects are not one file, and running the suite
after every command would be unbounded.

**You ask.** `/test` runs the same command off the event loop and prints the report. It is refused while a
run is active — the run is writing the tree the tests would read.

## Bounds

- **Timeout**: `timeout_ms`, default 120 s, clamped to 600 s. A command that outlives it is killed, and the
  report says **timed out**, which is not the same as a failing test. (`cmd.WaitDelay` bounds the wait after
  the kill, so a child that inherited the output pipe cannot hold the call open.)
- **Output**: `max_output_bytes`, default 4000, kept from the **end** — the failure is usually the last
  thing printed.
- **One run per tool call**: the auto mode never batches, retries or runs twice for the same write.

## What is not here

- **No test selection.** The whole discovered command runs; "only the tests this change affects" needs a
  dependency graph this increment does not build.
- **No coverage, no flaky-test detection, no retries.**
- **No sandbox.** The command runs with your permissions in the project root, exactly as if you typed it.
- **Discovery is Go/Node/Make.** Anything else is `test.command`.

## Where the code is

| Surface | Code |
| :--- | :--- |
| Discovery and the bounded run | `verify/verify.go` — `Discover`, `Run`, `Report` |
| The tool | `tool/tests.go` — `RunTests` |
| The auto mode | `agent/tests.go` — `runTestsForResult`, gated by `Agent.TestAfterWrite` |
| The command | `/test` in `tui/update.go`, `tui/test_command.go` |
