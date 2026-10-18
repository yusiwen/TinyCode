# The evaluation harness

This is a **spike, not a framework** (issue [#252](https://github.com/yusiwen/TinyCode/issues/252)). It
answers one question — does a measurement exist that can tell two agent configurations apart — and it
answers it with a number instead of an opinion. The framework itself is roadmap entry F1 and is still
unbuilt, on purpose: building it before knowing the signal exists would have been the expensive order.

## What it measures

Six small tasks, each a directory under [`eval/tasks/`](../eval/tasks) with a `go.mod`, a failing test and
a `PROMPT`. An attempt copies the task to a scratch directory, runs `tinycode "<prompt>"` there, and is
graded by the task's own `go test ./...` exit code — no judge model, no rubric, no text comparison.

Everything else comes from the attempt's **trajectory** (the record issue #249 added, documented in
[trajectories.md](trajectories.md)): steps, tokens, cost, the tools it called, and how it stopped. The
harness reads them through `agent.ListRuns`, so it is the record's second real consumer — if the format
were missing a field, this is what would notice.

Three configurations, chosen so the answer is not in doubt:

| Configuration | What it is | What it should do |
| --- | --- | --- |
| `build` | the default build agent, default budget | solve most tasks |
| `limited` | the same, with a hard token budget (`-limited-budget`, default 4000) | stop mid-task |
| `plan` | the read-only agent | fail, because it cannot write |

Each attempt gets a fresh copy of the task and its own record directory, so one attempt cannot help the
next one. Test files are hashed before and after: an attempt that edited a test is recorded as
**tampered** and counts as a failure, because a pass obtained by weakening the test is not a pass.

## Running it

```bash
make build
TINYCODE_EVAL=1 go run ./eval/runner                      # 3 configurations x 6 tasks x 2 reps = 36 runs
TINYCODE_EVAL=1 go run ./eval/runner -reps 3              # more repetitions, to read the noise
TINYCODE_EVAL=1 go run ./eval/runner -configurations build # one configuration, while iterating
```

It **spends money** and is therefore gated by `TINYCODE_EVAL=1`; nothing in CI sets it. The flags that
matter: `-tasks`, `-reps`, `-binary`, `-limited-budget`, `-timeout`, `-configurations`, `-work` (scratch,
default a fresh temporary directory) and `-out` (where `baseline.json` and `BASELINE.md` land; default
`eval`, which overwrites the committed baseline — that is the point).

## The verdict, and when to stop

The abandonment condition was written **before** the first measurement, and corrected once before any data
existed (the first version demanded `limited` strictly beat `plan`, which is unsatisfiable when both solve
nothing and `plan` is read-only by construction — a rule that fails for an arithmetic reason is not a
finding). It is three clauses:

1. `build` solves strictly more tasks than `limited`;
2. `limited` solves at least as many as `plan` — equality at zero is the expected floor;
3. `build` solves at least half the task set — otherwise what was measured is a floor, not a signal.

A task counts as **solved** when a majority of its repetitions passed. If any clause fails, F1 stops at
this spike: what gets built next is a harder task set, not a framework.

## The baseline, and what it says

`eval/BASELINE.md` is the committed report; `eval/baseline.json` is the same data per attempt. The
measurement of 2026-10-18 (36 runs, `deepseek-flash`):

| Configuration | Solved | Pass rate | Reps agreed | Median steps | Median tokens | Tokens per solved task |
| --- | --- | --- | --- | --- | --- | --- |
| `build` | 6/6 | 100% | 100% | 5 | 22 246 | 56 512 |
| `limited` (10 000) | 5/6 | 92% | 83% | 3 | 12 532 | 30 103 |
| `plan` | 0/6 | 0% | 100% | 4 | 12 595 | — |

**The signal exists, and the harness is stable**: `build` and `plan` agreed with themselves on every task,
and they are the two ends. **But the task set is saturated**: five of the six tasks are solved by step 3, so
a halved budget only bites on the hardest one (`parse-seconds`), which is also the only cell whose
repetitions disagreed. A framework built on this set would measure the ceiling rather than the agent.

Two things the records made legible, which is the argument for the record:

- One `build` attempt ended `stop: blocked` **and passed**. It had already written the fix, then tried
  `sandbox_allow` (a permission dialog a one-shot run cannot offer) and hit a terminal refusal — at
  **94 105 tokens, four times the median**. A `stop` of `blocked` is therefore not a synonym for failure,
  and a future framework has to grade on the outcome, not on how the run ended.
- The `plan` attempts burned ~12 600 tokens each to fail, and one hallucinated a tool name (`tool_use`),
  which the record keeps as a `tool.call` with an error status. Wasted spend is visible per configuration,
  which is the number that decides whether a read-only pass is worth running.

**What this says to do next**: build tasks with a difficulty spread, not a framework. The spike's job was
to find out whether the measurement separates; it does. Its second finding — that this task set cannot
separate at the top — is the more useful one.

## What it cannot say

- **Nothing about quality beyond pass/fail.** A task is solved or it is not; there is no partial credit, no
  judge, no rubric. That is deliberate for a spike (a judge is a second opinion to validate, and it costs
  money), but it means a framework would have to add it.
- **Nothing about a real repository.** The tasks are six files each. A task set that resembles real work —
  multi-file, ambiguous, with a test suite that has to be understood — is the next increment, and it is
  also where the cost per measurement goes up by orders of magnitude.
- **Nothing statistically.** Two or three repetitions give a noise *reading* (did the repetitions agree),
  not a confidence interval. Treat a one-task difference as suggestive, not significant.
- **Nothing about a model it did not run.** The baseline names the model it used; comparing a run on
  another model is a different measurement, not a better one.
