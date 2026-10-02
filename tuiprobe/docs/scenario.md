# Scenarios

A scenario is a text file that says what to do to a program and what the screen
should look like. It is the reproducible form of a manual check, and the shape CI
wants: one file, one exit code, artifacts that can be diffed.

```bash
tuiprobe run --dir tests tests/welcome.scenario          # compare
tuiprobe run --dir tests --update tests/welcome.scenario # re-baseline
tuiprobe run --gate TUI_SHOT tests/welcome.scenario      # skip unless gated on
```

Steps run in order against one session. **The first failing step stops the run and
names the line**, which is the point: a scenario failure reads like a review
comment, not a stack trace.

```
line 6: golden welcome_80x24.txt: artifact does not match tests/welcome_80x24.txt
first differing line 3 (column 12):
  want: "│ ready  │"
  got:  "│ reaady │"
```

## Steps

| Step | Meaning |
| --- | --- |
| `open [--size WxH] [--dir D] [--env K=V]… -- <command> [args…]` | start the program on a real terminal |
| `send [--text "…"] [--key NAME]…` | type text and/or press keys (`enter`, `ctrl+c`, `alt+left`, `f5`, …) |
| `wait --text <regexp> [--timeout 10s]` | wait until the screen matches |
| `stable 200ms [--timeout 10s]` | wait until the screen stops changing — better than a sleep |
| `sleep 500ms` | do nothing for a while (rarely what you want; `stable` usually is) |
| `golden <file>` | compare the plain-text screen against that file (normalized: no escapes, no trailing blanks) |
| `golden --ansi <file>` | compare the ANSI screen, escapes and all |
| `diff <file>` | the same as `golden`; both names are accepted |
| `fit --size WxH` | assert the session really is that size and the screen fits it |
| `close` | end the session and remember its exit code |
| `expect-exit N` | fail unless the program exited with `N` (closes first if needed) |
| `# comment` | ignored |

Quoting: use single or double quotes around an argument that contains spaces —
`open -- /bin/sh -c 'echo ready; read l'`. Everything after `--` belongs to the
program, even if it looks like one of our flags.

## Gating

`--gate VAR` makes a scenario a no-op that says so when `VAR` is unset:

```
$ tuiprobe run --gate TUI_SHOT tests/screenshots.scenario
skipped: TUI_SHOT is not set
$ echo $?
0
```

That is deliberate. A gated check that silently *passes* is worse than one that is
absent: it looks like evidence and is not. The exit code is 0 for a skip and the
reason is printed, so a CI log shows which checks really ran.

## What a scenario is not

- It is not a screenshot tool (yet): images arrive with the renderers.
- It is not a test framework: there are no loops, variables or conditionals. When a
  scenario needs logic, write it in the language you are testing and call
  `tuiprobe` commands from there — the CLI is the API.
