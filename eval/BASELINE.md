# The evaluation signal, measured

Generated 2026-10-18T08:39:00Z. `TINYCODE_EVAL=1 go run ./eval/runner`

36 runs, 2 repetitions per (configuration x task).

Models: deepseek-flash

| Configuration | Solved | Pass rate | Reps agreed | Median steps | Median tokens | Tokens per solved task | Stops |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `build` | 6/6 | 100% | 100% | 5 | 22246 | 56512 | final 11, blocked 1 |
| `limited` | 5/6 | 92% | 83% | 3 | 12532 | 30103 | budget 12 |
| `plan` | 0/6 | 0% | 100% | 4 | 12595 | 0 | final 12 |

**Verdict.** The signal moves — build 6/6, limited 5/6, plan 0/6. An evaluation framework has something to measure; whether it should be built is a separate argument, and the task set's own gradient is what it turns on.

## Per task

| Task | `build` | `limited` | `plan` |
| --- | --- | --- | --- |
| `average-compile` | 2/2 | 2/2 | 0/2 |
| `count-off-by-one` | 2/2 | 2/2 | 0/2 |
| `dedupe` | 2/2 | 2/2 | 0/2 |
| `parse-seconds` | 2/2 | 1/2 | 0/2 |
| `reverse-words` | 2/2 | 2/2 | 0/2 |
| `roman` | 2/2 | 2/2 | 0/2 |

## What the numbers are

- **Solved** counts tasks where a majority of repetitions passed, graded by the task's own `go test ./...`.
- **Reps agreed** is the fraction of tasks where every repetition reached the same verdict: the noise floor, not a confidence interval.
- **Tokens per solved task** is the whole configuration's token spend divided by its solved tasks, including what the failed attempts burned.
- A run whose test files were edited counts as a failure and is marked `tampered`: a pass obtained by weakening the test is not a pass.
- Metrics come from the run's trajectory (`agent.ListRuns`), not from this runner's own bookkeeping.
