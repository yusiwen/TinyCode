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

`testdata/scenarios/echo.scenario` in this module is a runnable example — it drives `/bin/sh`, so it
depends on no other repository — and `make test-tuiprobe-examples` runs it.

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
| `golden --against <file>` | the same comparison, for a caller that assembles the flags rather than the arguments |
| `diff <file>` | the same as `golden`; both names are accepted |
| `screenshot <file> [--scale N] [--font FILE] [--format png\|html] [--renderer font\|chromium] [--browser PATH]` | write the screen to a PNG (default) or HTML and **read it back**: the pixel size is asserted against this session's geometry, so the image is evidence of that geometry |
| `shot <file> [flags]` | the same step as `screenshot`; both names are accepted |
| `fit --size WxH` | assert the session really is that size and the screen fits it |
| `wait-exit [duration]` | wait for the program to end **on its own** and remember its code (default 10s) — use this before `expect-exit` when the program leaves by itself |
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

- It is not an image comparison tool: `screenshot` writes an artifact and verifies its size, but
  nothing diffs one image against a committed one. Text and ANSI screens are what a golden compares.
- It is not a test framework: there are no loops, variables or conditionals. When a
  scenario needs logic, write it in the language you are testing and call
  `tuiprobe` commands from there — the CLI is the API.
