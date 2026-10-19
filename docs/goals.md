# Goals: one objective, continued while you are not watching

A goal is one completion objective a session keeps working toward after a turn ends. It is the
difference between a conversation and a piece of work: the transcript records what happened, the goal
records **what is still owed**, and the agent starts another round toward it whenever the session goes
idle.

This is roadmap entry D7 (issues [#259](https://github.com/yusiwen/TinyCode/issues/259)). The design
record is in [roadmap.md](roadmap.md).

## Create one

```text
/goal make the failing test pass
```

The model can also create one on its own: the build agent's system prompt tells it to create a goal
before starting work that clearly will not fit in one turn — a migration, a multi-file refactor,
fixing every failing test or every issue matching a label — and to call `complete` or `blocked` when
the work ends. That is a judgement the model makes from your wording, not a classifier, so it is not
a guarantee; `/goal` is the deterministic path. Plan mode cannot create one (its read-only ruleset
denies the `goal` tool), so a long-running request usually becomes a plan and then a goal after
`/build`.

Either way there is **at most one goal per session** — an unfinished one has to be
edited, paused, completed, blocked or cleared before another replaces it.

`/goal` with no argument shows the current objective, the round count, the phase, and whether
continuation is armed:

```text
Goal: make the failing test pass
Round: 2/256
Phase: active, disarmed (say "continue" or /goal resume)
```

## What a round is

A round is **one whole turn**: dozens of tool calls, minutes of work. It is not a step and not a
single message. Each round starts with a fixed block naming the objective, the round number, the
authority for what to believe, and how the goal may end:

```text
<goal_round>
Objective: "make the failing test pass"
Round: 3/256

Continue working toward the objective.
Treat the workspace, the results of your own tool calls, and durable state (files, the session) as the authority: ...
Before you call the goal complete, produce evidence — a command, a test, or a file you can point at — ...
</goal_round>
```

The instruction to trust the workspace rather than a memory of earlier rounds is deliberate: a resumed
session restores the conversation, not the tool transcript, so the files are the only reliable account
of what was done.

## Who may do what

| Action | A turn you started | An automatic round | A sub-agent |
| --- | --- | --- | --- |
| Read the goal | yes | yes | yes |
| Create, edit, pause, resume | yes | **no** | no |
| Complete, or record a blocker | yes | yes | no |

A round may finish the work or report it blocked; it may not redefine what the work is. That is what
keeps the objective yours. The rule is checked before anything is changed, so a refusal never mutates
the goal.

## When it stops

- **`complete`** — the model decided the objective is achieved, with evidence in its reply.
- **`blocked`** — with a stable code and a reason:
  - `round-limit`: the round budget is spent.
  - `no-progress`: three consecutive rounds made no tool call. A model that only narrates would
    otherwise spend every round of the budget saying nothing.
  - `model-reported`: the model named a concrete condition that keeps blocking progress.
- **`paused`** — you pressed Ctrl+C during a round, or the round ended with an error. Cancellation
  never restarts a round, and a failed round is not retried automatically.
- Every one of these is written into the transcript, so a goal never stops silently. Long stops are
  also announced through the same completion channel as a finished run.

## After a restart, a resume or a fork

The goal survives: objective, phase and the rounds already spent are stored in the session file. The
**permission to continue does not**. Every start, `--resume` and `/fork` comes back disarmed, and only
a human arms it again:

- `/goal resume`, or
- saying "continue" — the model may re-arm it on a direct human request.

A fork keeps the objective and the rounds (it is another attempt at the same work) and is disarmed
too. Two branches can both be resumed, and each spends its own rounds.

## Cost

One round is a whole turn, so the default 256-round cap is a large ceiling, not a target. Two things
bound it in practice:

- `budget.max_tokens_per_session` stops the run that would cross the line. **Set this before starting
  a goal you intend to leave alone**; the session token budget will usually end a goal long before its
  round cap.
- `/goal pause` stops continuation immediately. A round already running is interrupted with Ctrl+C,
  which also pauses the goal.

The status bar shows `goal n/256` (with `blocked` or `done`) beside the token counter, so the round
and the spend are visible together.

## Configuration

```json
"goal": {
  "max_goal_rounds": 256,
  "idle_grace_seconds": 2,
  "no_progress_rounds": 3
}
```

| Key | Default | Meaning |
| --- | --- | --- |
| `max_goal_rounds` | 256 | Rounds one goal may run. A create may name a smaller cap; this is the default. |
| `idle_grace_seconds` | 2 | How long the session must be idle before a round starts. A draft in the input box always wins; this is the second chance for someone already typing. |
| `no_progress_rounds` | 3 | Consecutive tool-free rounds before the goal is blocked. |

A positive value replaces the default. Zero, absent or negative means the default — a typo is not a
way to remove a guard. The grace period exists because a run refuses concurrent input: a round that
started while you were typing would lock you out for its whole duration.

## What it does not do

- **No mid-run steering.** A round in flight refuses input. Pause it (Ctrl+C) and then type.
- **No independent verification.** "Complete" is the model's claim with evidence, not a judge's
  verdict. A future evaluator is a separate idea (roadmap F1).
- **No goal history.** `/goal clear` removes the goal; the transcript keeps what its rounds did.
- **Not available in plan mode.** The read-only ruleset denies the goal tool, so a plan-mode session
  cannot create one.
- **Not in one-shot mode.** A goal is session state, and `tinycode "prompt"` has no session to keep it
  in. The tool is registered only in the interactive path.
