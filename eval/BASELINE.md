# The evaluation signal, measured

Generated 2026-11-17T09:52:42Z. `TINYCODE_EVAL=1 go run ./eval/runner`

108 runs, 2 repetitions per (configuration x task).

Knobs: `-limited-budget 4000`, `-timeout 180s`.

Models: deepseek-flash

| Configuration | Solved | Pass rate | Reps agreed | Median steps | Median tokens | Tokens per solved task | Killed | Stops |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `build` | 17/18 | 97% | 94% | 5 | 28695 | 178476 | 0 | final 34, blocked 1, error 1 |
| `limited` | 0/18 | 0% | 100% | 1 | 4111 | 0 | 0 | budget 36 |
| `plan` | 0/18 | 0% | 100% | 5 | 15974 | 0 | 0 | final 33, error 3 |

**Verdict.** The signal moves — build 17/18, limited 0/18, plan 0/18. An evaluation framework has something to measure; whether it should be built is a separate argument, and the task set's own gradient is what it turns on.

## A/B comparison

Rule: a variant is better when it solves strictly more tasks and solves every task the other solved; any other difference is inconclusive. Solve status is by majority of repetitions, the same unit the tables above count; a task solved by exactly one side is listed, so a winner on count alone cannot hide a lost task.

- **build vs limited** — winner: **build**. build solves 17 task(s) limited does not, and keeps every task limited solves
- **build vs plan** — winner: **build**. build solves 17 task(s) plan does not, and keeps every task plan solves
- **limited vs plan** — both variants solved the same tasks

## pass@k

The probability that at least one of k attempts solves a task, averaged over the tasks that have at least k attempts (the unbiased estimator, `1 - C(n-c,k)/C(n,k)`, where `n` is a task's attempts and `c` its passes). It is **not** a per-attempt success rate: with fewer than k attempts a task says nothing about that row and is left out. A killed attempt counts in `n` and not in `c`.

| k | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| 1 | 97% | 0% | 0% |
| 2 | 100% | 0% | 0% |

## The gradient

| Tier | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| 1 | 6/6 | 0/6 | 0/6 |
| 2 | 3/3 | 0/3 | 0/3 |
| 3 | 8/9 | 0/9 | 0/9 |

| Tokens per solved task | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| 1 | 97600 | — | — |
| 2 | 136253 | — | — |
| 3 | 386611 | — | — |

**Gradient.** The set is harder at the top than at the bottom — tier 1 6/6 (100%), tier 3 8/9 (89%). A change can now be measured against a difficulty axis instead of a flat pass rate.

## Headroom

1 of 18 tasks defeated the default configuration and 17 were still solved: expr-eval.

## Difficulty, measured

| Task | Declared tier | Measured band | Median tokens | Median steps | Solved |
| --- | --- | --- | --- | --- | --- |
| `expr-eval` | 3 | expensive | 522652 | 29 | 1/2 |
| `duration-spec` | 3 | expensive | 292083 | 21 | 2/2 |
| `rollback` | 3 | expensive | 280330 | 25 | 2/2 |
| `reverse-words` | 1 | expensive | 167281 | 20 | 2/2 |
| `t2-inventory` | 2 | expensive | 145648 | 19 | 2/2 |
| `coupled-api` | 3 | expensive | 138126 | 15 | 2/2 |
| `breadth-fixes` | 3 | middle | 103847 | 13 | 2/2 |
| `merge-inversions` | 3 | middle | 78614 | 12 | 2/2 |
| `t3-extension` | 3 | middle | 56954 | 10 | 2/2 |
| `t3-cache` | 3 | middle | 41624 | 7 | 2/2 |
| `t3-boundary` | 3 | middle | 32216 | 6 | 2/2 |
| `t2-report` | 2 | middle | 30695 | 6 | 2/2 |
| `count-off-by-one` | 1 | cheap | 28695 | 6 | 2/2 |
| `t2-ledger` | 2 | cheap | 28037 | 5 | 2/2 |
| `parse-seconds` | 1 | cheap | 25787 | 5 | 2/2 |
| `roman` | 1 | cheap | 23911 | 5 | 2/2 |
| `average-compile` | 1 | cheap | 23827 | 5 | 2/2 |
| `dedupe` | 1 | cheap | 23299 | 5 | 2/2 |

The bands are thirds of this table's own token order, so they describe this run and not a label. A task whose measured band differs from its declared tier is a task whose label was a guess.

## Per task

| Task | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| `average-compile` (t1) | 2/2 | 0/2 | 0/2 |
| `breadth-fixes` (t3) | 2/2 | 0/2 | 0/2 |
| `count-off-by-one` (t1) | 2/2 | 0/2 | 0/2 |
| `coupled-api` (t3) | 2/2 | 0/2 | 0/2 |
| `dedupe` (t1) | 2/2 | 0/2 | 0/2 |
| `duration-spec` (t3) | 2/2 | 0/2 | 0/2 |
| `expr-eval` (t3) | 1/2 | 0/2 | 0/2 |
| `merge-inversions` (t3) | 2/2 | 0/2 | 0/2 |
| `parse-seconds` (t1) | 2/2 | 0/2 | 0/2 |
| `reverse-words` (t1) | 2/2 | 0/2 | 0/2 |
| `rollback` (t3) | 2/2 | 0/2 | 0/2 |
| `roman` (t1) | 2/2 | 0/2 | 0/2 |
| `t2-inventory` (t2) | 2/2 | 0/2 | 0/2 |
| `t2-ledger` (t2) | 2/2 | 0/2 | 0/2 |
| `t2-report` (t2) | 2/2 | 0/2 | 0/2 |
| `t3-boundary` (t3) | 2/2 | 0/2 | 0/2 |
| `t3-cache` (t3) | 2/2 | 0/2 | 0/2 |
| `t3-extension` (t3) | 2/2 | 0/2 | 0/2 |

The process (roadmap F4)
  What the run did, from its trajectory — not what it achieved, which is the grade above.

  configuration  runs avg steps avg calls  read%  verify%   repeat%  errors%
  build           36       9.5      10.2    33%      75%      2.2%     0.8%
  limited         36       1.0       2.0     0%       0%      0.0%     0.0%
  plan            36       4.6       6.5   100%       0%      0.0%    22.2%

  Does the process predict the outcome? (within each configuration: `plan` never
  verifies and `limited` runs out of budget first, so pooling them would compare
  configurations rather than runs.)

  configuration property                           with    pass  without    pass
  build        hit a tool error                      3    100%       33     97%
  build        read before writing                  12     92%       24    100%
  build        repeated a call                       8    100%       28     96%
  build        verified after the last write        27    100%        9     89%
  limited      hit a tool error                      0      0%       36      0%
  limited      read before writing                   0      0%       36      0%
  limited      repeated a call                       0      0%       36      0%
  limited      verified after the last write         0      0%       36      0%
  plan         hit a tool error                     34      0%        2      0%
  plan         read before writing                  36      0%        0      0%
  plan         repeated a call                       0      0%       36      0%
  plan         verified after the last write         0      0%       36      0%

  A cell with three attempts is a sentence, not a measurement: read the counts, not the percentage.

## What the numbers are

- **Solved** counts tasks where a majority of repetitions passed, graded by the task's own `go test ./...`.
- **Reps agreed** is the fraction of tasks where every repetition reached the same verdict: the noise floor, not a confidence interval.
- **Tokens per solved task** is the whole configuration's token spend divided by its solved tasks, including what the failed attempts burned.
- A run whose test files were edited counts as a failure and is marked `tampered`: a pass obtained by weakening the test is not a pass.
- Metrics come from the run's trajectory (`agent.ListRuns`), not from this runner's own bookkeeping.
