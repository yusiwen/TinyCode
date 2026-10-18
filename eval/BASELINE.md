# The evaluation signal, measured

Generated 2026-10-18T10:16:57Z. `TINYCODE_EVAL=1 go run ./eval/runner`

108 runs, 2 repetitions per (configuration x task).

Models: deepseek-flash

| Configuration | Solved | Pass rate | Reps agreed | Median steps | Median tokens | Tokens per solved task | Stops |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `build` | 18/18 | 100% | 100% | 6 | 33616 | 184183 | final 35, blocked 1 |
| `limited` | 6/18 | 50% | 67% | 3 | 12987 | 78292 | budget 36 |
| `plan` | 0/18 | 0% | 100% | 5 | 17932 | 0 | final 34, error 1 |

**Verdict.** The signal moves — build 18/18, limited 6/18, plan 0/18. An evaluation framework has something to measure; whether it should be built is a separate argument, and the task set's own gradient is what it turns on.

## The gradient

| Tier | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| 1 | 6/6 | 5/6 | 0/6 |
| 2 | 3/3 | 0/3 | 0/3 |
| 3 | 9/9 | 1/9 | 0/9 |

| Tokens per solved task | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| 1 | 65054 | 30486 | — |
| 2 | 240708 | — | — |
| 3 | 366335 | 243628 | — |

**Gradient.** **The condition for this increment is met, against calling the set graded.** tier 1 6/6 (100%), tier 3 9/9 (100%): tier 3 is solved as often as tier 1, so the new tasks are longer, not harder. What gets built next is a difficulty axis that actually binds.

## Headroom

**The condition for this increment is met, against the set having headroom.** no task defeated the default configuration (18 solved).

## Difficulty, measured

| Task | Declared tier | Measured band | Median tokens | Median steps | Solved |
| --- | --- | --- | --- | --- | --- |
| `expr-eval` | 3 | expensive | 465719 | 28 | 2/2 |
| `duration-spec` | 3 | expensive | 430429 | 22 | 2/2 |
| `coupled-api` | 3 | expensive | 314929 | 24 | 2/2 |
| `t2-inventory` | 2 | expensive | 161647 | 21 | 2/2 |
| `rollback` | 3 | expensive | 149342 | 17 | 2/2 |
| `t2-report` | 2 | expensive | 125961 | 18 | 2/2 |
| `breadth-fixes` | 3 | middle | 124313 | 15 | 2/2 |
| `average-compile` | 1 | middle | 82509 | 14 | 2/2 |
| `t2-ledger` | 2 | middle | 73454 | 11 | 2/2 |
| `t3-boundary` | 3 | middle | 55971 | 10 | 2/2 |
| `t3-extension` | 3 | middle | 42826 | 8 | 2/2 |
| `t3-cache` | 3 | middle | 32719 | 6 | 2/2 |
| `merge-inversions` | 3 | cheap | 32260 | 6 | 2/2 |
| `parse-seconds` | 1 | cheap | 24389 | 5 | 2/2 |
| `roman` | 1 | cheap | 22502 | 5 | 2/2 |
| `count-off-by-one` | 1 | cheap | 21981 | 5 | 2/2 |
| `dedupe` | 1 | cheap | 21946 | 5 | 2/2 |
| `reverse-words` | 1 | cheap | 21835 | 5 | 2/2 |

The bands are thirds of this table's own token order, so they describe this run and not a label. A task whose measured band differs from its declared tier is a task whose label was a guess.

## Per task

| Task | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| `average-compile` (t1) | 2/2 | 2/2 | 0/2 |
| `breadth-fixes` (t3) | 2/2 | 0/2 | 0/2 |
| `count-off-by-one` (t1) | 2/2 | 2/2 | 0/2 |
| `coupled-api` (t3) | 2/2 | 0/2 | 0/2 |
| `dedupe` (t1) | 2/2 | 1/2 | 0/2 |
| `duration-spec` (t3) | 2/2 | 0/2 | 0/2 |
| `expr-eval` (t3) | 2/2 | 0/2 | 0/2 |
| `merge-inversions` (t3) | 2/2 | 1/2 | 0/2 |
| `parse-seconds` (t1) | 2/2 | 2/2 | 0/2 |
| `reverse-words` (t1) | 2/2 | 2/2 | 0/2 |
| `rollback` (t3) | 2/2 | 0/2 | 0/2 |
| `roman` (t1) | 2/2 | 2/2 | 0/2 |
| `t2-inventory` (t2) | 2/2 | 1/2 | 0/2 |
| `t2-ledger` (t2) | 2/2 | 0/2 | 0/2 |
| `t2-report` (t2) | 2/2 | 1/2 | 0/2 |
| `t3-boundary` (t3) | 2/2 | 2/2 | 0/2 |
| `t3-cache` (t3) | 2/2 | 1/2 | 0/2 |
| `t3-extension` (t3) | 2/2 | 1/2 | 0/2 |

## What the numbers are

- **Solved** counts tasks where a majority of repetitions passed, graded by the task's own `go test ./...`.
- **Reps agreed** is the fraction of tasks where every repetition reached the same verdict: the noise floor, not a confidence interval.
- **Tokens per solved task** is the whole configuration's token spend divided by its solved tasks, including what the failed attempts burned.
- A run whose test files were edited counts as a failure and is marked `tampered`: a pass obtained by weakening the test is not a pass.
- Metrics come from the run's trajectory (`agent.ListRuns`), not from this runner's own bookkeeping.
