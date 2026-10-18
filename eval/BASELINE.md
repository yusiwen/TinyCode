# The evaluation signal, measured

Generated 2026-10-18T09:18:55Z. `TINYCODE_EVAL=1 go run ./eval/runner`

72 runs, 2 repetitions per (configuration x task).

Models: deepseek-flash

| Configuration | Solved | Pass rate | Reps agreed | Median steps | Median tokens | Tokens per solved task | Stops |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `build` | 12/12 | 100% | 100% | 5 | 27033 | 88091 | final 21, blocked 3 |
| `limited` | 9/12 | 83% | 83% | 3 | 12829 | 34453 | budget 24 |
| `plan` | 0/12 | 0% | 100% | 4 | 13560 | 0 | final 23, error 1 |

**Verdict.** The signal moves — build 12/12, limited 9/12, plan 0/12. An evaluation framework has something to measure; whether it should be built is a separate argument, and the task set's own gradient is what it turns on.

## The gradient

| Tier | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| 1 | 6/6 | 5/6 | 0/6 |
| 2 | 3/3 | 1/3 | 0/3 |
| 3 | 3/3 | 3/3 | 0/3 |

| Tokens per solved task | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| 1 | 98055 | 30532 | — |
| 2 | 198562 | 78670 | — |
| 3 | 62870 | 26778 | — |

**Gradient.** **The condition for this increment is met, against calling the set graded.** tier 1 6/6 (100%), tier 3 3/3 (100%): tier 3 is solved as often as tier 1, so the new tasks are longer, not harder. What gets built next is a difficulty axis that actually binds.

## Per task

| Task | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| `average-compile` (t1) | 2/2 | 2/2 | 0/2 |
| `count-off-by-one` (t1) | 2/2 | 2/2 | 0/2 |
| `dedupe` (t1) | 2/2 | 2/2 | 0/2 |
| `parse-seconds` (t1) | 2/2 | 2/2 | 0/2 |
| `reverse-words` (t1) | 2/2 | 1/2 | 0/2 |
| `roman` (t1) | 2/2 | 2/2 | 0/2 |
| `t2-inventory` (t2) | 2/2 | 1/2 | 0/2 |
| `t2-ledger` (t2) | 2/2 | 0/2 | 0/2 |
| `t2-report` (t2) | 2/2 | 2/2 | 0/2 |
| `t3-boundary` (t3) | 2/2 | 2/2 | 0/2 |
| `t3-cache` (t3) | 2/2 | 2/2 | 0/2 |
| `t3-extension` (t3) | 2/2 | 2/2 | 0/2 |

## What the numbers are

- **Solved** counts tasks where a majority of repetitions passed, graded by the task's own `go test ./...`.
- **Reps agreed** is the fraction of tasks where every repetition reached the same verdict: the noise floor, not a confidence interval.
- **Tokens per solved task** is the whole configuration's token spend divided by its solved tasks, including what the failed attempts burned.
- A run whose test files were edited counts as a failure and is marked `tampered`: a pass obtained by weakening the test is not a pass.
- Metrics come from the run's trajectory (`agent.ListRuns`), not from this runner's own bookkeeping.
