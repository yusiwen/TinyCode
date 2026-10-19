# Trajectories

A trajectory is the machine-readable record of one run: which model was asked,
how many steps it took, what each step cost in time and tokens, which tools ran
and how they ended. It exists because "did this change make the agent better"
needs data, and none of the other artifacts are data: a session is the
conversation (kept for resuming it), `tlog` is a log for a human reading a
failure, and the status bar shows one number at a time.

It is the raw material for an evaluation harness; it is not one. There is no task
set, no pass@k and no judge here — that is roadmap entry F1, which reads these
files.

## Where it lives, and how to read it

```bash
tinycode --trajectories                 # the newest 20 runs, one line each
tinycode --trajectory 20261018T071926Z-r1   # one run's file, verbatim
```

The files are the export. `--trajectories` reads them rather than an index, so it
is also the format's first consumer: a field the writer stops carrying shows up
there before it shows up in a script somebody wrote.

```
~/.tinycode/trajectories/20261018T071926Z-r1.jsonl
```

One file per run, named by the UTC second it started and a per-process counter.
A delegated agent writes its own file, named after the run that delegated it
(`…-r1.explore-1.jsonl`) and carrying that run's id in `parent`. The directory is
`trajectory.dir`; recording itself is switched off with `trajectory.enabled:
false`.

## The format

JSON lines: one JSON object per line, one line per event, append-only. Every line
carries `v` (the schema version, currently `1`), `type`, `run`, `at` (RFC3339,
UTC) and, when known, `parent`, `session` and `agent`.

| `type` | Written when | Carries |
| --- | --- | --- |
| `run.start` | the run opens, before anything takes time | `provider`, `model`, `capture`, `prompt` (digest), `goal` (`goal_id`, `round`, `source`) when the run serves a goal |
| `llm.call` | a provider call returns, one per call | `step`, `duration_ms`, `ttft_ms` (only when something streamed), `outcome` (`final`/`tool_calls`/`empty`), `tokens`, `cost` |
| `tool.call` | one tool call finishes, in call order | `step`, `call`, `tool`, `args` (digest), `result` (digest), `duration_ms`, `status` |
| `step.end` | the step that made the call finishes | `step`, `duration_ms` |
| `run.end` | the run stops, whatever the reason | `stop`, `steps`, `duration_ms`, `tokens`, `cost`, `cost_unknown_calls` |

`goal` is present on a run that serves a session goal, and `source` says who started it: `driver` for
a round the continuation driver began, `human` for a turn a person did. It is what lets an evaluation
separate automatic rounds from human work without inferring it from the prompt.

`stop` is the outcome, and it is the field an evaluation groups by:

| `stop` | Meaning |
| --- | --- |
| `final` | the model answered and the loop ended |
| `budget` | a token budget stopped the run before another call |
| `blocked` | a terminal refusal ended the run (a capability the host lacks, a configured rule) |
| `step_limit` | the step budget ran out and the forced summary was asked for |
| `cancelled` | the context was cancelled |
| `error` | the provider call or the summary call failed |

**Sizes and digests.** `prompt`, `args` and `result` are `{sha256, chars}`, so two
runs can be compared and deduplicated without reading either payload. With
`capture: "full"` they also carry `text`, bounded at 64 KiB per payload; `chars`
always reports the true length.

**Durations** are milliseconds, measured with the agent's own clock: `llm.call`
runs from the instant the request was sent — the same instant its price is keyed
on — and `tool.call` from the instant the tool started. `ttft_ms` is the delay to
the first streamed delta of any kind (reasoning or text), and it is absent, not
zero, when nothing streamed.

**Costs** use the vocabulary the status bar uses: `cost.source` is `reported`
(the route said what it charged), `declared` (computed from a rate you declared)
or `unknown` (neither — which is not the same as free). `run.end.cost` is an array
of `{unit, amount}` rather than an object so two files diff cleanly.

**Indices.** `step` is 0-based and `call` is the index within the step; a `step`
of one run joins with the checkpoint store's manifests, which use the same
numbering. `run.end.steps` is a *count*, so it is one higher than the last `step`.

## Three queries

```bash
# Which runs spent the most, and on what
jq -s 'map(select(.type=="run.end")) | sort_by(.tokens.total_tokens) | reverse | .[0:5]
       | map({run, stop, tokens: .tokens.total_tokens, cost})' ~/.tinycode/trajectories/*.jsonl

# Which tools are slow, across every run
jq -s 'map(select(.type=="tool.call")) | group_by(.tool)
       | map({tool: .[0].tool, calls: length, mean_ms: (map(.duration_ms)|add/length)})' \
   ~/.tinycode/trajectories/*.jsonl

# Every run a budget stopped, newest first
jq -s 'map(select(.type=="run.end" and .stop=="budget")) | reverse | .[].run' \
   ~/.tinycode/trajectories/*.jsonl
```

## What it records, and what it deliberately does not

**Digests by default, text on request.** A trajectory is written on every run, to
disk, and tool arguments carry secrets (an API key exported into a command) while
results carry file contents. `capture: "metadata"` is the default: a digest and
a length, enough to dedup and to compare runs, not enough to leak a transcript.
`capture: "full"` is honoured **only when it comes from your own config file**
(`~/.tinycode/config.json`); a project-local `.tinycode/config.json` that asks for
it is ignored and the ignore is logged, because a cloned repository must not be
able to decide that your prompts get written to disk. The same file may switch
recording off — off is not a widening.

**The store is not a tool path.** A tool that tries to write under the trajectory
directory is refused with `ask: reserved`, exactly like the checkpoint store:
a record the recorded agent can rewrite is not evidence. `sandbox_full_access`
is the documented exception.

**A recording failure never fails a run.** This is the opposite of the checkpoint
rule on purpose. The checkpoint store refuses a write it cannot make undoable —
that is safety. A trajectory refuses only to record: a full disk warns once, the
run still answers, and nothing about the answer changes.

**Retention.** Past `trajectory.max_runs` (default 200) or `max_bytes` (default
64 MiB), the oldest files are removed, checked once per run when it closes. A run
that is still being written is never removed.

## What it cannot say

- **An interrupted run has no `run.end`.** Every line up to the kill is there —
  that is what append-only buys — and `--trajectories` prints such a run as
  `incomplete` rather than as broken. A reader must not require the last line.
- **`tool.call.status` is classified from the result text**, because the text is
  what the loop has: the dispatcher turns every failure into a string for the
  model to read. The vocabulary is `ok`, `error`, `blocked` (a terminal refusal),
  `refused` (a refusal the model can act on), `repeated` (the repeat circuit
  breaker). A tool whose own output begins with `error` is therefore recorded as
  a failure — an honest reading of a string the loop cannot distinguish from one
  it wrote.
- **No CPU time, no memory, no per-message tokens.** Steps, wall-clock durations,
  token totals per call and per run, and tool outcomes are what it measures.
- **No task set.** It records what happened; judging whether that was good is F1,
  and it needs tasks, repetitions and a comparison, none of which live here.
- **A nested run is visible only through its parent.** A sub-agent's file carries
  `parent`; nothing pools its numbers into the parent's `run.end`, so a total for
  "this run including delegation" has to be summed from the files. That is G1's
  attribution problem, and pretending otherwise would report a number that cannot
  be computed.
