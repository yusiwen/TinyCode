# The evaluation signal, measured

Generated 2026-11-17T13:46:38Z. `TINYCODE_EVAL=1 go run ./eval/runner`

108 runs, 2 repetitions per (configuration x task).

Knobs: `-limited-budget 10000`, `-timeout 180s`.

Models: deepseek-flash

| Configuration | Solved | Pass rate | Reps agreed | Median steps | Median tokens | Tokens per solved task | Killed | Stops |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `build` | 17/18 | 97% | 94% | 6 | 33039 | 150290 | 0 | final 35, error 1 |
| `limited` | 10/18 | 61% | 89% | 3 | 14019 | 50547 | 0 | budget 36 |
| `plan` | 0/18 | 0% | 100% | 4 | 14309 | 0 | 0 | final 32, error 4 |

**Verdict.** The signal moves — build 17/18, limited 10/18, plan 0/18. An evaluation framework has something to measure; whether it should be built is a separate argument, and the task set's own gradient is what it turns on.

## Regression against the baseline

Rule: a task is regressed when the baseline solved it in every attempt and this run solved it in none; improved is the mirror; every other case is inconclusive.

No regressions and no improvements the rule can decide.

Unchanged: 35 task(s) decided; inconclusive: 19 (a partial pass on one side).

## A/B comparison

Rule: a variant is better when it solves strictly more tasks and solves every task the other solved; any other difference is inconclusive. Solve status is by majority of repetitions, the same unit the tables above count; a task solved by exactly one side is listed, so a winner on count alone cannot hide a lost task.

- **build vs limited** — winner: **build**. build solves 7 task(s) limited does not, and keeps every task limited solves
- **build vs plan** — winner: **build**. build solves 17 task(s) plan does not, and keeps every task plan solves
- **limited vs plan** — winner: **limited**. limited solves 10 task(s) plan does not, and keeps every task plan solves

## pass@k

The probability that at least one of k attempts solves a task, averaged over the tasks that have at least k attempts (the unbiased estimator, `1 - C(n-c,k)/C(n,k)`, where `n` is a task's attempts and `c` its passes). It is **not** a per-attempt success rate: with fewer than k attempts a task says nothing about that row and is left out. A killed attempt counts in `n` and not in `c`.

| k | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| 1 | 97% | 61% | 0% |
| 2 | 100% | 67% | 0% |

## The gradient

| Tier | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| 1 | 6/6 | 6/6 | 0/6 |
| 2 | 3/3 | 1/3 | 0/3 |
| 3 | 8/9 | 3/9 | 0/9 |

| Tokens per solved task | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| 1 | 54996 | 27248 | — |
| 2 | 75220 | 86624 | — |
| 3 | 354777 | 86458 | — |

**Gradient.** The set is harder at the top than at the bottom — tier 1 6/6 (100%), tier 3 8/9 (89%). A change can now be measured against a difficulty axis instead of a flat pass rate.

## Headroom

1 of 18 tasks defeated the default configuration and 17 were still solved: expr-eval.

## Difficulty, measured

| Task | Declared tier | Measured band | Median tokens | Median steps | Solved |
| --- | --- | --- | --- | --- | --- |
| `merge-inversions` | 3 | expensive | 326233 | 27 | 2/2 |
| `rollback` | 3 | expensive | 305832 | 24 | 2/2 |
| `duration-spec` | 3 | expensive | 292605 | 24 | 2/2 |
| `coupled-api` | 3 | expensive | 191642 | 21 | 2/2 |
| `expr-eval` | 3 | expensive | 111519 | 13 | 1/2 |
| `t3-boundary` | 3 | expensive | 67493 | 11 | 2/2 |
| `breadth-fixes` | 3 | middle | 47675 | 7 | 2/2 |
| `t3-cache` | 3 | middle | 46490 | 7 | 2/2 |
| `t2-inventory` | 2 | middle | 46017 | 8 | 2/2 |
| `t2-ledger` | 2 | middle | 41257 | 7 | 2/2 |
| `parse-seconds` | 1 | middle | 36819 | 7 | 2/2 |
| `count-off-by-one` | 1 | middle | 33039 | 7 | 2/2 |
| `t3-extension` | 3 | cheap | 29620 | 6 | 2/2 |
| `t2-report` | 2 | cheap | 25556 | 5 | 2/2 |
| `roman` | 1 | cheap | 24137 | 5 | 2/2 |
| `dedupe` | 1 | cheap | 23898 | 5 | 2/2 |
| `average-compile` | 1 | cheap | 23611 | 5 | 2/2 |
| `reverse-words` | 1 | cheap | 23486 | 5 | 2/2 |

The bands are thirds of this table's own token order, so they describe this run and not a label. A task whose measured band differs from its declared tier is a task whose label was a guess.

## Per task

| Task | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| `average-compile` (t1) | 2/2 | 2/2 | 0/2 |
| `breadth-fixes` (t3) | 2/2 | 0/2 | 0/2 |
| `count-off-by-one` (t1) | 2/2 | 2/2 | 0/2 |
| `coupled-api` (t3) | 2/2 | 0/2 | 0/2 |
| `dedupe` (t1) | 2/2 | 2/2 | 0/2 |
| `duration-spec` (t3) | 2/2 | 0/2 | 0/2 |
| `expr-eval` (t3) | 1/2 | 0/2 | 0/2 |
| `merge-inversions` (t3) | 2/2 | 2/2 | 0/2 |
| `parse-seconds` (t1) | 2/2 | 2/2 | 0/2 |
| `reverse-words` (t1) | 2/2 | 2/2 | 0/2 |
| `rollback` (t3) | 2/2 | 0/2 | 0/2 |
| `roman` (t1) | 2/2 | 2/2 | 0/2 |
| `t2-inventory` (t2) | 2/2 | 1/2 | 0/2 |
| `t2-ledger` (t2) | 2/2 | 0/2 | 0/2 |
| `t2-report` (t2) | 2/2 | 2/2 | 0/2 |
| `t3-boundary` (t3) | 2/2 | 2/2 | 0/2 |
| `t3-cache` (t3) | 2/2 | 1/2 | 0/2 |
| `t3-extension` (t3) | 2/2 | 2/2 | 0/2 |

The process (roadmap F4)
  What the run did, from its trajectory — not what it achieved, which is the grade above.

  configuration  runs avg steps avg calls  read%  verify%   repeat%  errors%
  build           36       8.9       9.9    42%      72%      3.4%     0.3%
  limited         36       3.0       4.7    47%       0%      0.0%     0.0%
  plan            36       4.4       6.5   100%       0%      0.0%    24.0%

  Does the process predict the outcome? (within each configuration: `plan` never
  verifies and `limited` runs out of budget first, so pooling them would compare
  configurations rather than runs.)

  configuration property                           with    pass  without    pass
  build        hit a tool error                      1    100%       35     97%
  build        read before writing                  15    100%       21     95%
  build        repeated a call                      12    100%       24     96%
  build        verified after the last write        26    100%       10     90%
  limited      hit a tool error                      0      0%       36     61%
  limited      read before writing                  17     59%       19     63%
  limited      repeated a call                       0      0%       36     61%
  limited      verified after the last write         0      0%       36     61%
  plan         hit a tool error                     33      0%        3      0%
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
