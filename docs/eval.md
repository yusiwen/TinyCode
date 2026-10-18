# The evaluation harness

This is a **spike, not a framework** (issue [#252](https://github.com/yusiwen/TinyCode/issues/252)). It
answers one question — does a measurement exist that can tell two agent configurations apart — and it
answers it with a number instead of an opinion. The framework itself is roadmap entry F1 and is still
unbuilt, on purpose: building it before knowing the signal exists would have been the expensive order.

## What it measures

Twelve tasks, each a directory under [`eval/tasks/`](../eval/tasks) with a `go.mod`, a failing test, a
`PROMPT` and a declared `TIER`. An attempt copies the task to a scratch directory, runs
`tinycode "<prompt>"` there, and is graded by the task's own `go test ./...` exit code — no judge model, no
rubric, no text comparison.

**The tiers are the difficulty axis**, and they are declared rather than inferred:

| Tier | What a task in it looks like | Tasks |
| --- | --- | --- |
| 1 | one file, one obvious defect, and a prompt that restates the goal | 6 |
| 2 | several files, a defect that spans a contract between them, or two defects that only together keep the suite red | 3 |
| 3 | several files, a prompt that names only the *symptom*, and a suite whose edge cases are the specification | 3 |

The first increment of this harness (issue #252) shipped only tier 1, and the measurement said so: five of
six tasks were solved by step 3, so the set could not tell a full budget from a halved one except on the
hardest task. Tier 2 and tier 3 are that finding's answer, and the harness now reports the pass rate **per
tier**, which is the only way to see whether the answer worked.

**Every task has a reference fix**, in [`eval/_solutions/`](../eval/_solutions) — deliberately *not* inside
the task directory, because `copyTask` would hand it to the agent. `go test ./eval/runner` proves both
halves for every task: red before the fix, green after it, and the fix never touches a test file. A task
nobody can solve measures the task, not the agent.

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

`eval/BASELINE.md` is the committed report; `eval/baseline.json` is the same data per attempt. The graded
measurement of 2026-10-18 (72 runs, `deepseek-flash`):

| Configuration | Solved | Pass rate | Reps agreed | Median steps | Median tokens | Tokens per solved task |
| --- | --- | --- | --- | --- | --- | --- |
| `build` | 12/12 | 100% | 100% | 5 | 27 033 | 88 091 |
| `limited` (10 000) | 9/12 | 83% | 83% | 3 | 12 829 | 34 453 |
| `plan` | 0/12 | 0% | 100% | 4 | 13 560 | — |

**The signal exists, and it is stable**: `build` and `plan` agreed with themselves on every task, and they
are the two ends. **The declared gradient does not exist**: `build` solves 6/6 of tier 1 and 3/3 of tier 3.
The tiers are not what makes a task hard.

The numbers say something more useful than "the tasks were too easy", and they are worth reading before
anyone labels a task again:

| Tier | `build` solved | `build` tokens per solved task | `limited` solved |
| --- | --- | --- | --- |
| 1 | 6/6 | 98 055 | 5/6 |
| 2 | 3/3 | 198 562 | 1/3 |
| 3 | 3/3 | 62 870 | 3/3 |

- **The spread inside tier 1 is wider than any gap between tiers.** The single most expensive `build`
  attempt is `t2-inventory` (164 207 tokens), and the second is a **tier-1** task — `count-off-by-one`,
  133 666 tokens over 17 steps — while `dedupe`, also tier 1, costs a fraction of that. A *declared* tier is
  a guess about difficulty; the measurement is not.
- **What the halved budget bites on is an invariant that has to survive a refusal.** Its failures are
  concentrated in tier 2 — `t2-ledger` 0/2, `t2-inventory` 1/2 — the tasks where a rejected operation must
  leave state untouched (a refused transfer may not have moved the balances; a refused reservation may not
  have reserved). The one other failure is a tier-1 task (`reverse-words`, 1/2), which reads as noise rather
  than as difficulty. File count, prompt ambiguity and "read the tests first" were not obstacles at all:
  tier 3 solved 3/3 under the same budget that cut tier 2 to 1/3.
- **Cost is not monotone with tier**, which kills the idea that a harder tier is simply a longer one.

So the next difficulty axis is not "more files" and not "vaguer prompt": it is **state that must survive a
failure path**, and the next set should be built around that — with the tiers *measured* from the first
run's steps and tokens rather than declared in advance.

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

## The gradient

The report has a section per tier: solved counts and tokens per solved task, for each configuration. The
question it answers was stated in issue [#254](https://github.com/yusiwen/TinyCode/issues/254) before any
data existed:

> **`build` must solve a strictly smaller fraction of tier 3 than of tier 1.**

Fractions, not counts, because the tiers do not hold the same number of tasks: three of three is not
"harder" than six of six. If the condition fails, the new tasks are longer rather than harder, the tiers are
mislabelled, or the difficulty axis is somewhere else — and what gets built next is that axis, not a
framework.

Read the tokens-per-solved-task rows together with the pass rates. A tier that costs three times as much to
solve is a different kind of difficulty from one that simply fails more often, and a future evaluator will
want to know which one it is measuring.
