# The evaluation harness

This is a **spike, not a framework** (issue [#252](https://github.com/yusiwen/TinyCode/issues/252)). It
answers one question — does a measurement exist that can tell two agent configurations apart — and it
answers it with a number instead of an opinion. The framework itself is roadmap entry F1 and is still
unbuilt, on purpose: building it before knowing the signal exists would have been the expensive order.

## What it measures

Eighteen tasks, each a directory under [`eval/tasks/`](../eval/tasks) with a `go.mod`, a failing test, a
`PROMPT` and a declared `TIER`. (A separate corpus under `eval/spike-repo-index/` exists for the
repository-index spike: six tasks whose tests are **hidden** until grading, and a `map` configuration the
default runs never select. [repo-index-spike.md](repo-index-spike.md) is its result.) An attempt copies the task to a scratch directory, runs
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

## Repository-level tasks

The eighteen tasks above are hand-written modules, and three increments of evidence say why that is the
limit of what they can measure: the difficulty *knobs* move cost, not success. Issue
[#332](https://github.com/yusiwen/TinyCode/issues/332) takes the other route the earlier conclusions named —
**the domain** — with tasks built from this repository's own fix history.

A task is a directory under `eval/repo-tasks/<name>/` with two files:

```
TASK          # base: <sha>, fix: <sha>, packages: ./hook, hidden: hook/hook_test.go
PROMPT        # the symptom, without naming a file or a test
```

| Part | Where it comes from |
| :--- | :--- |
| the tree the agent gets | `git archive <base>` — a real 100-file repository, materialized at run time |
| the grade | the test files the **fix** commit changed, injected after the attempt (`hidden:`) |
| the reference fix | the fix commit's non-test diff, used only by the offline gate |
| what the agent sees | the prompt; the tests never reach the work copy |

Two properties follow, and both matter:

- **The tests are hidden**, so the agent has to find the defect in a real tree rather than read the assertion
  it must satisfy. The base tree also has no `.git`, so the fix commit is not reachable from the work copy.
- **The grade is the fix's own tests**, so the task measures "did the agent reach the behaviour the fix
  reached", not "did it write the change we had in mind".

Grading runs `go test <packages>` rather than `./...`: the whole suite takes minutes and is not what the task
is about.

### Running one

```bash
make build
cp bin/tinycode /tmp/tinycode-under-test     # the harness path quirk; see repo-index-spike.md
TINYCODE_EVAL=1 go run ./eval/runner \
  -repo-tasks eval/repo-tasks -repo . -configurations build -reps 2 \
  -out /tmp/repo-eval -work /tmp/repo-eval-work -binary /tmp/tinycode-under-test
```

`-repo` names the git repository the commits are read from; the tree is never copied into the repository, so
a task is reproducible only against this repository's history.

### The gate that runs without money

`go test ./eval/runner -run RepoTask` materializes one task, injects the fix's tests, proves it **red**, and
proves the reference patch makes it **green** — no provider, no network. The corpus's second task is proved
the same way by hand (`eval/repo-tasks/../check` is not committed; the PR for #332 records both halves).

### The first measurement

Two tasks, `build`, two repetitions each (2026-11-06):

| Task | Solved | Cost |
| :--- | :--- | :--- |
| `hook-timeout` | **2/2** (50 steps each) | ~1.55M and 1.58M tokens |
| `root-threading` | **0/2** (one hit the 50-step limit; the other was killed by the attempt timeout and recorded no steps) | 2.56M tokens for the recorded attempt |
| `run-compaction` | **0/2** (one hit the 50-step limit; the other killed by the attempt timeout) | 3.33M tokens for the recorded attempt |

Across the set: `build` solved **1/3**, every repetition agreed on its verdict, the median attempt hit the
**50-step limit**, and **tokens per solved task were 9.0M** — against 88k–184k on the module-level corpus. The
second measurement, with the third task added, is the one that met this increment's condition:

> **at least two tasks defeat the default configuration.**

Two of three did. What that means, said precisely:

- **The step budget is the binding constraint.** Every recorded run ended at 50 steps; the two hard tasks were
  not solved *within the agent's configured budget*, which is a defeat and also a statement about the budget.
- **Two attempts were killed by the harness timeout** (900 s). They count as failures, and the runner now
  records them as such ([#336](https://github.com/yusiwen/TinyCode/issues/336)): the result carries
  `killed: true` and `stop: "timeout"`, the report has a **Killed** column, and the median-steps,
  median-tokens and tokens-per-solved-task rows exclude them — a killed attempt never reached a stop, so
  what it recorded is a prefix, not a result. A study that leaves them in would read a zero as a
  measurement.
- **The domain costs an order of magnitude more**: 9M tokens per solved task means a regression baseline on
  this corpus has to be paid for deliberately, not on every change.

**One of the two defeated the funded agent**, so the domain lever is real — and the increment's
pre-registered condition ("at least two tasks defeat `build`") was **not met**. Two facts are worth keeping:

- `hook-timeout` was too easy for the repository it sits in: the fix is the same idiom the codebase already
  uses in `tool/bash.go`, and the agent found it in one pass. A repository-level task still has to be hard
  *for that repository*.
- `root-threading` defeated the agent by being a **cross-cutting change**: several files derive the root for
  one call, and the agent's first attempt patched one of them and reasoned about "what remains" at the step
  limit. That is the shape the F1 entry has been pointing at since the first increment — a failure mode that
  is not "one package, one test suite".

What follows, per the condition: the set needs more tasks of the second shape (and fewer of the first) before
anything is claimed about "a framework"; a framework built on two tasks would measure the pair, not the agent.

The third task, `run-compaction`, was added after the first measurement: a test written for a behaviour no fix
commit carries (`hidden/agent/runcompaction_test.go` in the task directory), against a work copy at
`0f4eea7` with the compaction of a run's own messages removed. The reference is the production diff of
`65d35d4`. All three tasks are red before and green after their reference, checked by the same script the
offline gate uses. Its measured run is in the table above.

### What it does not do

- **Two tasks, not a benchmark.** They exist to test whether the domain lever works at all.
- **The hidden tests must compile against the base tree**, or "red" would mean "the test does not build".
  Both current tasks satisfy this, and it is the first thing to check when adding a task.
- **The base tree is not committed**: reproducibility depends on this repository's history, which is the
  point of the exercise and also its limit.
