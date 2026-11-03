# Hooks

Roadmap C4. A hook is a command you declare in **your own** `~/.tinycode/config.json` and the agent runs
at a tool or run boundary. It is how "run gofmt after a `.go` write" or "run the gate before a commit"
becomes a setting instead of a habit.

## Declaring one

```json
{
  "hooks": {
    "pre_tool_use":  [{"command": "test ! -e .no-writes", "match": "write_file"}],
    "post_tool_use": [{"command": "gofmt -w .", "match": "write_file"}],
    "stop":          [{"command": "notify-send 'run finished'"}],
    "session_end":   [{"command": "cp ~/.tinycode/log/latest.log /tmp/last-run.log"}]
  }
}
```

| Field | Meaning |
| :--- | :--- |
| `command` | Run with `sh -c` in the project root, so a pipeline is allowed |
| `timeout_ms` | Bound for this hook; default 5000, clamped to 60000 |
| `match` | Glob over the tool name for the two tool events; empty means every tool |

## The four events

| Event | When | What a non-zero exit does |
| :--- | :--- | :--- |
| `pre_tool_use` | before a tool executes | **refuses the call**: the hook's output becomes the tool result the model reads |
| `post_tool_use` | after a tool returned | nothing; the failure is logged and the tool's result is unchanged |
| `stop` | when a run returns, success or failure | nothing; logged |
| `session_end` | once, when the process is done with the session | nothing; logged |

## What a hook receives

One JSON object on stdin:

```json
{"event":"post_tool_use","tool":"write_file","args":"{\"path\":\"a.go\"}","result":"wrote 12 bytes"}
```

`args` and `result` are **clipped** (500 and 1000 bytes), because a tool result can be megabytes and a
hook that needs the file can read it. `error` is set when the tool failed. `session` and `step` are
present when the run knows them.

## What is guaranteed

- **Only your own config may declare a hook.** A project-local `.tinycode/config.json` that adds one is
  ignored, and the start-up log says so: a cloned repository must not be able to run a command on your
  machine because you opened it.
- **Every hook is bounded** by `timeout_ms`; the one that outlives it is killed and the refusal names the
  timeout.
- **A failed `pre_tool_use` hook refuses the call.** A spawn failure, a timeout or a non-zero exit are all
  refusals: a guard that fails open is not a guard. `post_tool_use`, `stop` and `session_end` report their
  failures and change nothing.
- **Output is truncated** at 2000 bytes before it reaches the transcript or the model.
- **Hooks run in order**, and the first `pre_tool_use` refusal stops the rest.

## What is not guaranteed

- **No sandbox.** A hook runs with your permissions and environment, exactly like a command you typed. It
  is not confined by the agent's file boundary, and it can reach the network.
- **No worktree isolation** with parallel sub-agents: two hooks can run at the same time in the same
  directory.
- **No retry and no ordering between events**: a `post_tool_use` hook that fails is not retried, and
  nothing waits for it before the next step.

## Recipes

**Format after a write.** `post_tool_use` with `match: "write_file|edit|apply_patch"` and
`command: "gofmt -w ."`. On a large tree, narrow the command to the file the hook received
(`jq -r .args` through `$1`-style parsing is the hook's job, not the agent's).

**Refuse writes to generated files.** `pre_tool_use` with `match: "write_file"` and
`command: "test ! -e .generated"` — exit non-zero to refuse, and print the reason on stdout.

**Run the gate before a commit.** `pre_tool_use` with `match: "git_commit"` and `command: "make lint"`.

**Report at the end of a run.** `stop` with a command that reads the payload from stdin (`jq` is not a
dependency of this repository; the payload is plain JSON).

## Where the code is

| Surface | Code |
| :--- | :--- |
| The runner | `hook/hook.go` — `Fire`, `run`, timeout and truncation |
| The config rule | `config.Layers.ResolveHooks` (user layer only) |
| The boundaries | `agent/hooks.go` — `preToolHook`, `postToolHook`, `stopHook` |
| The listing | `/hooks` in `tui/update.go` |
