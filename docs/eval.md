# The evaluation harness

This is a **spike, not a framework** (issue [#252](https://github.com/yusiwen/TinyCode/issues/252)). It
answers one question — does a measurement exist that can tell two agent configurations apart — and it
answers it with a number instead of an opinion. The framework itself is roadmap entry F1 and is still
unbuilt, on purpose: building it before knowing the signal exists would have been the expensive order.

## What it measures

Eighteen tasks, each a directory under [`eval/tasks/`](../eval/tasks) with a `go.mod`, a failing test, a
`PROMPT` and a declared `TIER`. An attempt copies the task to a scratch directory, runs
`tinycode "<prompt>"` there, and is graded by the task's own `go test ./...` exit code — no judge model, no
rubric, no text comparison.

**The tiers are the difficulty axis**, and they are declared rather than inferred:

| Tier | What a task in it looks like | Tasks |
| --- | --- | --- |
| 1 | one file, one obvious defect, and a prompt that restates the goal | 6 |
| 2 | several files, a defect that spans a contract between them, or two defects that only together keep the suite red | 3 |
| 3 | several files, a prompt that names only the *symptom*, and a suite whose edge cases are the specification — plus, since the third increment, tasks chosen to be hard for a stated reason: an algorithmic bound the suite times, an implementation from a specification, five defects at once, one wire format produced by three sites, all-or-nothing state, and an exhaustive format with a round-trip | 9 |

The history is in the measurements. The first increment (issue #252) shipped six tier-1 tasks, and five were
solved by step 3. The second (issue #254) added declared tiers, and the reported gradient did not appear. The
third (issue #256) stopped trusting labels: `TIER` is now an *expectation*, and the report carries a
**measured band** per task — thirds of the default configuration's own token spend — so the document shows
where the label and the measurement disagree.

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

`eval/BASELINE.md` is the committed report; `eval/baseline.json` is the same data per attempt. The third
measurement of 2026-10-18 (108 runs, 4 413 153 tokens, about $0.86 on `deepseek-flash`):

| Configuration | Solved | Pass rate | Reps agreed | Median steps | Median tokens | Tokens per solved task |
| --- | --- | --- | --- | --- | --- | --- |
| `build` | 18/18 | 100% | 100% | 6 | 33 616 | 184 183 |
| `limited` (10 000) | 6/18 | 33% | 67% | 3 | 12 987 | 78 292 |
| `plan` | 0/18 | 0% | 100% | 5 | 17 932 | — |

**The condition for this increment failed: no task defeated the default configuration.** That was written
down before the run, with the conclusion attached, so it is not an excuse afterwards — **the knob is the
problem, not the setting of it**. Six tasks, each chosen from a different hypothesis about what defeats this
agent, all fell: the algorithmic bound, the specification to implement, the five defects at once, the three
coupled sites, the all-or-nothing state, the exhaustive format. `build` solved every one, two repetitions out
of two.

What the increment *did* show is where the difficulty went, and it is not where the labels were:

| Task | Declared tier | Measured band | Median tokens | Median steps |
| --- | --- | --- | --- | --- |
| `expr-eval` | 3 | expensive | 465 719 | 28 |
| `duration-spec` | 3 | expensive | 430 429 | 22 |
| `coupled-api` | 3 | expensive | 314 929 | 24 |
| `t2-inventory` | 2 | expensive | 161 647 | 21 |
| `rollback` | 3 | expensive | 149 342 | 17 |
| `merge-inversions` | 3 | **cheap** | 32 260 | 6 |

- **The task I expected to be hardest was the cheapest.** `merge-inversions` — an O(n log n) bound the suite
  enforces with its own clock — was solved in 6 steps: the model wrote a counting merge sort immediately.
  What cost the most was the opposite: implementing `expr-eval` from a specification (28 steps, 465 719
  tokens, 25 times the cheapest task) and the exhaustive format spec.
- **The difficulty knob moves cost, not success.** Tokens per solved task went from 88 091 in the previous
  twelve-task set to 184 183 here, and the spread across tasks is 18 440 to 465 719 — but the pass rate is
  unchanged at 100%.
- **It does defeat a starved agent.** `limited` fell from 9/12 to 6/18 with the same budget, and every one of
  its runs hit the budget. So the tasks are harder in the sense that they cost more than a small budget
  allows; they are not harder in the sense that defeats a funded agent.
- **`build` still ends some runs `stop: blocked` while passing** (one this time): the terminal-refusal path is
  still not a synonym for failure.

**So the next thing to change is the domain, not the difficulty knob**: a real repository, a multi-hour
refactor, a task whose failure mode is not "one package, one test suite". That is written in
`docs/roadmap.md` and in issue #256's condition, before this data existed.

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
