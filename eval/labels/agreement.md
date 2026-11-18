# Agreement, measured

The arithmetic behind [`../rubric.md`](../rubric.md)'s decision rule. Two agreements were fixed before the
labels existed: rule-versus-label on the two dimensions that have a deterministic counterpart, at **≥ 80%**;
and consistency of the labels themselves, at **quadratic-weighted κ ≥ 0.60**.

## Rule versus label

The label is mapped to a boolean at the rubric's own boundary (score ≥ 1 means "did it"). The rule's boolean
is `process.read_before_write` and `process.verified`, computed by `eval/runner/process.go` over the same
trajectory the digest came from.

| Attempt | D1 label | D1 agrees before | D1 agrees after | D3 label | D3 agrees before | D3 agrees after |
| --- | --- | --- | --- | --- | --- | --- |

(*agrees* = the rule's boolean matches the label mapped at score ≥ 1; it is not the rule's value)
| `build/average-compile` rep0 | 2 | ✅ | ✅ | 2 | ❌ | ✅ |
| `build/coupled-api` rep0 | 2 | ✅ | ✅ | 2 | ✅ | ✅ |
| `build/expr-eval` rep0 | 2 | ❌ | ✅ | 0 | ✅ | ✅ |
| `build/expr-eval` rep1 | 2 | ❌ | ✅ | 2 | ✅ | ✅ |
| `build/merge-inversions` rep0 | 2 | ❌ | ✅ | 2 | ✅ | ✅ |
| `build/t3-extension` rep0 | 2 | ❌ | ✅ | 2 | ❌ | ✅ |
| `limited/average-compile` rep0 | 2 | ❌ | ✅ | 0 | ✅ | ✅ |
| `limited/breadth-fixes` rep0 | 2 | ❌ | ✅ | 0 | ✅ | ✅ |
| `limited/duration-spec` rep0 | 2 | ✅ | ✅ | 0 | ✅ | ✅ |
| `limited/t2-inventory` rep0 | 2 | ❌ | ✅ | 0 | ✅ | ✅ |
| `plan/average-compile` rep0 | 2 | ✅ | ✅ | 0 | ✅ | ✅ |
| `plan/t3-extension` rep0 | 2 | ✅ | ✅ | 0 | ✅ | ✅ |

| Agreement | Before the fixes | After the fixes | Threshold |
| --- | --- | --- | --- |
| D1 evidence gathering | **5/12 (42%)** | **12/12 (100%)** | ≥ 80% |
| D3 verification | 10/12 (83%) | **12/12 (100%)** | ≥ 80% |

**The threshold did its job: it rejected the rule as written.** Two defects, both real, both invisible from
the unit tests:

1. **D1 counted only the `read_file` and `search_files` tools.** These agents read with `cat` — eleven of
   the twelve attempts gathered their evidence in `bash`, and the rule scored them as never having looked.
   Fixed: a `bash` call whose command is `cat`, `head`, `tail`, `grep`, `rg`, `wc`, `less`, `more`, `bat`,
   or `awk` (or `sed` without `-i`) is evidence gathering.
2. **D3 counted only `bash` commands containing `go test`.** A run that verified through the `run_tests`
   tool was scored as never having verified — two of the twelve. Fixed: `run_tests` always counts.

Both fixes are in `eval/runner/process.go` with a test each, and `eval/baseline.json`'s process data was
regenerated from the kept trajectories with the corrected rule (the report's `verify%` for `build` went
from 72% to 89%).

## Consistency of the labels

**Not measured, and this is a deviation the study records rather than hides.** The second pass was supposed
to be blind. It could not be: the labels were made by the same reader, in the same session, from the same
twelve digests, and re-reading a digest after labelling it is not a blind trial. A fabricated κ would be
worse than none, so none is reported.

The consequence is decisive for the decision rule: **the κ ≥ 0.60 clause cannot be evaluated**, so the
rubric is not certified, and no judge may be built against it yet. What the labels *can* say is why the
sample cannot support the measurement:

- Ten of the twelve attempts score 2 on every dimension that applies to them. With that little variation,
  even a perfect second pass would leave κ undefined (chance agreement equals observed agreement).
- The variation that exists is in **how far the attempt got**, not in how well it worked: five attempts were
  cut off by the token budget or the provider before a change or a verification was possible. Those are the
  only non-2 scores, and they are 0 or 1 by construction, not by choice.
- There is no attempt in the sample that skipped evidence gathering, and none that claimed a pass it had
  not checked. Those are the cases the dimensions exist to catch, and the corpus has none of them.

## The decision

- **Rule versus label: pass** (100% on both dimensions) — but only after the two fixes, and only on a sample
  with no negative cases. What it establishes is that the rule no longer *false-negatives* a careful run; it
  does not establish that the rule catches a careless one.
- **Label consistency: not established** — the blind second pass was not made.
- **Verdict: no-go for a judge, for now.** Not because the rubric is wrong, but because the corpus cannot
  test it: a corpus where every attempt behaves well distinguishes nothing, and a judge validated on it
  would be certified by agreement on cases where agreement is free.

What would change the verdict, in order:

1. A corpus with **bad-process attempts** — runs that write before reading, that claim a pass they did not
   check, that scatter unrelated edits. The current rungs are "the default agent, which does it well" and
   "a budget that stops it before it can". A rung in between, or tasks hard enough that the default agent
   flails, is what the study needs.
2. A **second labeller** for the same twelve attempts, so κ measures something. The rubric and this page
   are the material that makes that cheap: the digests regenerate from a recorded run, and the anchors are
   written to be disagreed with.
3. Only then a judge model, scored against the human labels rather than against itself.
