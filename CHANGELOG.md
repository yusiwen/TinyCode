# Changelog

TinyCode is developed as a sequence of small, verifiable changes. This file
records them: the work that is not yet tagged as a release first, then the
historical feature log that used to live at the end of `README.md`.

Entries keep the commit hashes they landed with, so a line here can be traced
back to its change and tests.

## Unreleased

### A goal you can watch, and a round you can attribute

Issue #259, S5 — the surfaces. The status bar carries `goal n/256` (with `blocked` or `done` when it
stops) and sits **above the spend** in priority, so an 80-column terminal keeps the round counter and
drops the money — a driver-managed session needs to know whether it is still running more than it needs
the running total. A goal that stops announces itself through the same completion channel as a finished
run, gated by the same duration rule, and the run record carries `goal{goal_id, round, source}` on its
start line, so an evaluation can tell a driver round from a human turn.

The cross-process property finally has a gate of its own. `goal-resume.scenario` resumes a committed
session whose goal was written by a previous process, asserts the objective, `Round: 2/256`, the
disarmed hint and the status segment, then proves the session is idle by running `/goal pause`: a round
in flight would refuse the submission, so the phase line would never appear. It needs no model — the
base URL is unreachable on purpose, because an unintended request should fail loudly. Mutation-checked
by arming a restored goal: the scenario fails at `disarmed`.

### A goal that runs while nobody is watching

Issue #259, S4 — the idle continuation driver. A goal a human created starts a round of its own once
the session is idle, and each round is a whole turn: dozens of tool calls, minutes of work.

The decision is a pure function (`goal.Decide`), made twice: once when the tick is armed and again when
it arrives, because the user may have started typing during the grace period — a draft in the input box
wins, which is the interim answer to there being no mid-run steering yet. `Service.StartRound` is the
authority and re-checks under the lock that claims the round; the counter is persisted before the work,
so a crash loop cannot spend the budget twice.

Four ways a goal stops, and every one of them says so in the transcript: the round cap (`round-limit`),
three consecutive rounds that made no tool call (`no-progress`), a round that failed or was cancelled
(paused — cancellation never auto-restarts), and the model calling `complete` or `blocked`. The 2-second
grace, the no-progress threshold and the round cap are the knobs; S6 moves them into the config file.

Mutation-checked, four reverted: dropping the input guard starts rounds over a draft, dropping the
pause-on-error leaves a failed round armed, dropping the cap blocker leaves a spent goal active, and
dropping the context marker lets a round carry human authority.

### The model can see the goal, and cannot hand itself one

Issue #259, S3 — the `goal` tool. `get` returns `{goal, armed}`; `create`, `edit`, `pause`, `resume`,
`complete` and `blocked` mutate it, each echoing the `{goal_id, revision}` a `get` returned, so a stale
view is refused instead of overwriting newer state.

The authority rule is the substance. A human turn may create, edit, pause or resume; a goal round may
only read, complete or block — a round that could rewrite its own objective would be a machine deciding
what to work on next. A sub-agent may only read, because it was handed a unit of work inside a turn, not
authority over the session. The facts live in the context (`types.InGoalRound`, `types.InSubagent`),
stamped by the driver (S4) and by `tool/task.go` / `tool/bgtask.go`, and the tool checks them before the
service is touched, so a refusal mutates nothing.

The tool is registered only in the interactive path: a goal is session state, and a one-shot run has no
session to keep it in.

Mutation-checked, both reverted: ignoring the sub-agent marker lets a child create goals, and ignoring the
goal-round marker lets a round create or pause its own.

### A goal a person can see and steer

Issue #259, S2 — `/goal`: show, create, edit, pause, resume, clear. The human half of D7; the
model-facing tool is S3 and the driver is S4.

Two details are pinned rather than left to taste. Control words count only when they fill the whole
input, so `/goal pause after verification` creates that literal objective instead of pausing and
throwing the rest away. And the goal is written to the session at two safe points — every `/goal`
mutation and the end of every run — because the service is in-memory and the file is the only durable
log: a finished run now saves the transcript and the goal, instead of waiting for `/exit`.

`/goal` on a resumed session says `disarmed`: S1 already refuses to arm a restored goal, and this is
where the user reads that and re-arms it.

Mutation-checked, both reverted: dropping the goal from `persistSession` fails the create, pause and
resume tests, and removing the run-end save leaves a cleared goal on disk.

### A goal that survives the session, and never arms itself

Issue #259, S1 — the domain half of D7. One completion objective per session, with a durable phase
(`active` / `paused` / `blocked` / `complete`), a round budget and a revision fence, stored in the
session file and carried through a fork.

The split is the design. Everything a restart has to restore lives in `goal.Goal` and is written with
the session; whether work may continue *on its own* is not stored at all. `goal.Service.Restore`
disarms unconditionally, so a start, a resume and a fork all begin without permission to continue,
and only a human can arm one. A test asserts that no serialised goal carries an `armed` key, because
the property is the absence.

Two mutation checks, both reverted: arming on restore fails the disarmed paths
(`TestResumeRearmsAnActiveGoal`, `TestStartRound`, `TestShouldContinue`), and dropping the fork copy
loses the branch's goal (`TestForkCarriesTheGoal`).

### A resumed session reaches the model, and every ending leaves a trace

Issue #258, the prerequisite for D7. Two defects shared one consequence: a run that was not a clean final
answer left the model with no memory of it.

`--resume` rebuilt the transcript for the screen only. `Agent.History` — the context `Run` sends — was never
filled from the session, so the first prompt after a resume answered a conversation the user could read and
the model could not. It is now restored from the persisted user and assistant turns. Command output (the
`system` entries the interface renders) is skipped, and so is a previous turn's reasoning: the provider
resends `reasoning_content` verbatim, and a thinking block from an earlier turn is not input.

The other half was the endings that needed continuing most. Three early returns — the step limit, a token
budget and a terminal refusal — left `History` untouched, and the step limit left the session untouched too,
so the next run could not read what the previous one did. All four endings, the normal answer included, now
go through one `recordTurn`: it appends the pair to `History` and the session and flushes. A turn with no
assistant text is not recorded, because there is nothing a later run could read.

Two mutation checks, both reverted: removing the history append fails all three ending tests, and skipping
the session writes fails their session assertions. `TestResumedHistoryReachesTheFirstRequest` asserts order,
not presence — the restored turns come before the new prompt in the request the provider actually receives.

### Six hard tasks, and the finding that the knob is the problem

Roadmap F1, third increment (issue #256). The second increment ended with the tier labels refuted, and with a
worse fact: no task had ever made the default configuration fail. A set on which the agent always succeeds
cannot measure improvement, because there is no headroom.

So this increment built six tasks chosen to be hard for a *stated reason* — an algorithmic bound the suite
enforces with its own clock (`merge-inversions`), an implementation from a specification (`expr-eval`), five
defects at once (`breadth-fixes`), one wire format produced by three sites (`coupled-api`), all-or-nothing
state (`rollback`), an exhaustive format with a round-trip (`duration-spec`) — and it stopped trusting
labels: `TIER` is an *expectation* now, and the report carries a **measured band** per task, taken from thirds
of the default configuration's own token spend.

**No task defeated the default configuration.** 108 runs, `deepseek-flash`: `build` solved 18/18, two
repetitions each. The condition was written before the data with the conclusion attached, so this is not an
excuse afterwards: **the difficulty knob is the problem, not the setting of it.**

What the increment did find is where difficulty actually lives, and it is not where the labels were:

- **The task I expected to be hardest was the cheapest.** `merge-inversions`, with an O(n log n) bound the
  suite enforces by timing 200 000 elements, was solved in 6 steps — the model wrote a counting merge sort
  immediately. What cost the most was implementing `expr-eval` from a specification: 28 steps, 465 719 tokens,
  twenty-five times the cheapest task.
- **The knob moves cost, not success.** Tokens per solved task went from 88 091 to 184 183; the spread across
  tasks is 18 440 to 465 719; the pass rate did not move from 100%.
- **It does defeat a starved agent.** `limited` fell from 9/12 to 6/18 with the same 10 000-token budget, and
  every one of its runs hit the budget. Harder in the sense of "costs more than a small budget allows"; not
  harder in the sense that defeats a funded one.

**The next change is the domain, not the knob**: a real repository, a multi-hour refactor, a failure mode that
is not one package and one test suite. That conclusion is in issue #256's condition, in `docs/roadmap.md` and
in `docs/eval.md` — all written before this data existed (`78656f0`).

### A difficulty gradient, and the measurement that refused it

Roadmap F1, second increment (issue #254). The first measurement said the task set was saturated: five of six
tasks solved by step 3, so a halved budget bound only on the hardest one. The next increment was named in that
finding — a set with a difficulty gradient — and this is it: six more tasks in two declared tiers (multi-file
contracts; symptom-only prompts whose tests are the specification), a `TIER` per task, per-tier reporting, and
a reference fix for every task.

**The gradient does not exist.** Over 72 runs on `deepseek-flash`, `build` solves 6/6 of tier 1 and **3/3 of
tier 3**, so the tiers are not what makes a task hard. The numbers say something more useful than "the tasks
were too easy": the spread *inside* tier 1 (23k to 134k tokens for one task) is wider than any gap between
tiers, and the halved budget's failures are concentrated in tier 2 — `t2-ledger` 0/2 and `t2-inventory` 1/2 —
the tasks where a rejected operation must leave state untouched. File count, prompt ambiguity and "read the
tests first" were not obstacles.

Three things the increment added that outlive the result. **A reference fix per task**, kept in
`eval/_solutions/` (outside the task directory, because `copyTask` would hand it to the agent) and proved by a
test that every task is red before its fix and green after, never touching a test — a task nobody can solve
measures the task, not the agent. **A `TIER` that is required**, so a task cannot silently lose its label.
And **a second condition, pre-registered before the data**: `build` must solve a strictly smaller *fraction*
of tier 3 than of tier 1, fractions rather than counts because the tiers do not hold the same number of
tasks. It failed, and failing it is the deliverable: the next set is built around failure-path state and its
tiers are measured from the first run instead of declared (`51e379c`).

### A measurement before a framework

Roadmap entry F1 (issue #252). "Did the agent get better?" had no signal: 998 test functions prove
TinyCode's code works, never that the agent on top of it does anything useful. F1 is the largest lever in
the roadmap and the roadmap's own rule for its class of work is that the first increment must answer **"is
this worth it"** with a measurement — so this is the measurement, not the framework.

`eval/` holds six self-contained tasks (a `go.mod`, a real defect, a failing test, a one-line prompt) and a
runner that, for each (configuration × task × repetition), copies the task to a scratch directory, gives it
its own `.tinycode/config.json` (the agent mode, a hard token budget, the attempt's record directory), runs
the binary, and grades the attempt **by the task's own `go test ./...`** — no judge. Test files are hashed
before and after, so an attempt that weakens a test is recorded as tampered and counts as a failure.
Everything else comes from the attempt's trajectory, through `agent.ListRuns`, which makes the harness the
record's second real consumer.

The abandonment condition was written before the data and corrected once before any existed: the first
version demanded the starved configuration strictly beat the read-only one, which is unsatisfiable when both
solve nothing and the read-only agent cannot write by construction.

Measured 2026-10-18, 36 runs on `deepseek-flash`: **build 6/6, limited 5/6, plan 0/6**, with the two ends
agreeing with themselves on every task. The signal exists. But the task set is saturated — five of the six
tasks are solved by step 3, so a 10 000-token budget only bites on the hardest one, which is also the only
cell whose repetitions disagreed. **So the next increment is a task set with a difficulty spread, not a
framework.** Two things the records made legible, and the reason the record was worth building first: one
`build` attempt ended `stop: blocked` *and passed*, after trying a permission dialog a one-shot run cannot
offer, at four times the median token cost; and the read-only configurations burned ~12 600 tokens each to
fail, which is the number that decides whether such a pass is worth running (`d97c149`).

### Every run leaves a machine-readable record

Roadmap entry E4 (issue #249). A run left nothing a program could measure. The callbacks that drive the
display carry a tool name and an argument *summary* for the status line, no timing and no result; the
session store is the conversation, kept for resuming it; and `tlog`'s `"trace"` is a log level rather than
tracing. So "did this change make the agent better" had no data to answer with, which is the whole of
roadmap F1 waiting on a prerequisite.

A run now writes one JSONL file: one event per line, each carrying its own duration, so a reader never
pairs two lines to compute one. `run.start` names the route, the model and a digest of the prompt;
`llm.call` records each provider call's duration, time to first token, tokens, cost and outcome;
`tool.call` records the tool, digests of its arguments and result, its duration and a status;
`step.end` and `run.end` close the step and the run, and `run.end` names *why* it stopped — `final`,
`budget`, `blocked`, `step_limit`, `cancelled` or `error`. `tinycode --trajectories` lists the newest
runs and `--trajectory <id>` prints one file.

Three decisions are deliberate. Recording is on by default but keeps **metadata only** — an sha256 and a
length per payload — because it writes to disk on every run and tool arguments carry secrets;
`capture: "full"` is honoured only from your own config file, so a cloned repository cannot decide that
your prompts get written down. A recording failure never fails a run, which is the opposite of the
checkpoint rule on purpose: one is safety, the other observability. And the store is **not a tool path** —
`ask: reserved`, as with the pre-images — because a record the recorded agent can rewrite is not evidence.

A delegated agent writes its own file, numbered by the parent and linked through `parent`, which is the
data G1's per-agent attribution needs without claiming G1. The store is bounded at 200 runs / 64 MiB and
pruned once per run, never removing a file a run still holds open. A killed run has no `run.end`; that is
what append-only buys, and `--trajectories` reports such a run as incomplete rather than as broken
(`5024e09`, `bdba458`, `8ac904e`).

### Confine a shell command on macOS too

Roadmap entry C7 (issue #247). The path fence confines the agent's own opens, and on Linux the launcher
confines a command it spawns with Landlock — but on macOS nothing confined the command, so every `bash`
call ran under the substring checks alone. `confinement_other.go` answered the capability question false,
`confine_commands` defaulted off with it, and `/sandbox` said so. The asymmetry was a cost paid daily on
this project's own development machine.

macOS has a mechanism, and it cannot be applied from inside the process: the launcher renders a Seatbelt
profile and re-execs through `/usr/bin/sandbox-exec`, which becomes the command. The profile starts from
`(allow default)`, denies `file-write*`, keeps `/dev/null` writable (a plain `> /dev/null` is refused
otherwise) and re-allows each root with `(subpath …)`. Roots are resolved before they enter it: Seatbelt
matches the path the kernel resolved, and `/tmp`, `/var` and `/etc` are links into `/private`, so a rule
written for `/tmp/x` matches nothing while the command can still address the file as `/tmp/x`.

The scope is deliberately identical to Landlock's — write-class file access only — so the two platforms
mean the same thing by confinement; a profile that also refused sockets would stop `go mod download`.
Nothing in the config changed: the default was already derived from the host's capability rather than
from its name, so the mechanism landing is what turned `confine_commands` on. The capability answer is a
smoke run rather than a file check, because the launcher fails closed and a false positive would turn
confinement on and then refuse every command in the session.

`sandbox-exec` is deprecated by Apple, which is exactly why every failure path refuses the command
instead of running it unconfined: the worst case is a host that refuses commands, never a host that
silently frees them. `make test-tui-confine`, which skipped on macOS with a printed reason, now passes
there and asserts on the filesystem after the real binary exits (`749fa37`, `cb23310`, `bd74d6f`, `27d0707`).

### Price a call at the hour it was made

Roadmap entry A3's cost half (issue #225): a declared rate had five billing lanes and no time dimension,
so the provider this repository ships as its default — DeepSeek bills peak and off-peak — could not be
priced correctly by any number. `config.example.json` declared the peak rates and was therefore **2× too
high for about 79% of the week**, with nothing in the display saying so.

A declared entry now carries an optional list of `windows` beside its rates: `weekdays` (`mon`…`sun`, or
full names, any case), `from`/`to` as HH:MM in UTC (`from` inclusive, `to` exclusive, `00:00`/`24:00`
meaning midnight at the end of the day — LiteLLM writes DeepSeek's schedule that way), and the same five
lanes. The entry's own rates apply at every instant a window does not cover, so the smaller of the two
declarations is the one to write. A call is priced at the instant its request was sent, in UTC: a route
bills by the hour the request was made in, and a long stream can cross 04:00 while it is being generated.

The declaration is checked before anything is stored. A window has to repeat every lane its entry declares
(a lane left at zero inside a window would silently charge the entry's rate — half price, in exactly one
lane), windows may not overlap on a shared day, and they may not cover the whole week, which would make the
entry's own rates unreachable. Each refusal is a startup error naming the entry and the window, and a
refused entry prices nothing, so the call is reported unknown rather than priced by a guess.

The example now declares DeepSeek's real pair — off-peak on the entry, the two peak blocks
(01:00-04:00 and 06:00-10:00 UTC on weekdays, 35 of 168 hours) as windows — and
`pricing_config_test.go` prices that file at a fixed peak instant and a fixed off-peak one, lane by lane,
so the document is an executable part of the change and not a copy of it. **What it still cannot say**:
Chinese public holidays, which the vendor prices as off-peak and this schema prices at peak (2× too high on
those hours), because a holiday calendar changes every year; a window that crosses midnight, which has to be
written as two; and a displayed window name — the status bar still says `declared`, not which slice priced
the call (`ba60844`, `4f40ec3`).

### Undo a `bash` command's changes too

Roadmap entry C1's last boundary (issue #238): `/undo` covered the file tools' writes and nothing a
command did, because a shell command never says what it will touch. Before a command runs, the project
tree is now read, hashed and stored; after it, one stat walk (size, modification time, inode) finds the
paths that moved and records them as ordinary manifest entries, so `/undo` reaches a command exactly
like a file-tool call. A command that changed nothing writes no manifest.

The bounds are measured rather than guessed: **42.9 ms and 379 files** on this repository with the
2 MiB per-file cap (`go test ./tool -run XXX -bench CommandSnapshot`), which is what 32 MiB / 4000
files leaves an order of magnitude of headroom for. Over a bound the command still runs and its result
says the changes were not recorded. A file above the per-file cap that a command *creates* is still
recorded, because undoing it means removing it; one it *modifies* is not, and the result says so.
The bytes are copied rather than hard-linked on purpose: a link shares the inode, so `echo x > file`
would make the "pre-image" the new bytes, and a wrong pre-image is worse than a missing one.
`docs/checkpoints.md` carries the numbers, the limits, and the benchmark that reproduces them.

### Undo a tool call — and what that does not cover

Roadmap entry C1 (issue #234): a tool-mediated edit used to land straight on disk. Every file tool's write
now records the bytes it is about to replace, through the one function all of them pass through
(`writeSandboxed`, and `removeSandboxed` for `apply_patch`'s delete operation, which previously left nothing
to restore). `/undo` in the TUI lists the files and asks before restoring; `--undo` does the same without a
TUI. Marking the call undone in its own manifest is what makes a second undo reach the call before it, and a
restore that fails partway leaves the call unmarked so a retry is safe (`1a6c1d7`, `9795cb0`, `a21d9a0`,
`845b35e`, `2d5fe7e`, `80921f0`).

The store lives in `checkpoint_dir` (default `~/.tinycode/checkpoints`), deliberately outside the project
root so the fence that confines a tool write confines the recovery store too, and it is not a tool path: a
write to it is refused with `ask: reserved`, which no permission grants — only `sandbox_full_access`
bypasses it, because that mode means no boundary at all. It is bounded to 50 calls or 64 MiB per project,
dropping the oldest **already-undone** calls first and then collecting objects no remaining manifest
references; a call that can still be undone is never dropped, so the bound loses to undo-ability on purpose.
`config.example.json` documents the key, and `/sandbox` reports the store and its contents.

What it does **not** cover is documented rather than implied ([docs/checkpoints.md](docs/checkpoints.md)):
files changed by a `bash` command or any other subprocess are not captured, because nothing tells this layer
what such a command will touch. The store takes a second producer for them; that producer is its own piece
of work.

### The shipped example loads, and `$`-prefixed keys are documentation

`config.example.json` opens with "Copy to config.json" and could not survive that copy: its `agents` block
carries a `$comment`, `agents` decodes into a **map**, so the note was read as an entry and `json.Unmarshal`
failed with `cannot unmarshal string into Go struct field Config.agents.$comment of type
config.AgentOverride`. `loadFile` reported that on stderr and both callers read the error as "no file", so
a user who followed the instruction silently lost every section at once — five providers, `pricing`,
`sandbox`, `notify`, `budget` — and ran on the built-in defaults. Pre-existing, and invisible because no
test had ever read the template (issue #229).

`loadFile` now drops every object key whose name begins with `$` before decoding, at any depth, so the
convention the file teaches holds where it is written rather than only where the container happens to be a
struct. The four map positions are `agents`, `pricing.prices` and an MCP server's `env` and `headers`. The
walk carries values as `json.RawMessage`, so a number wider than a float64, and an escape such as the
`\u003cPROJECT_ROOT\u003e` that `json.Marshal` writes for an angle-bracket placeholder, survive it
unchanged.

Four tests in `config/shipped_example_test.go` hold the property: the template copied into a redirected
`HOME` loads through `LoadConfig()` with nothing on stderr and yields the file's own values; a `$comment`
inside `agents`, `prices`, `sandbox` and an MCP server's two maps is ignored rather than becoming an entry;
the walk is recursive on a document deeper than any section; and the raw round trip `AddAllowedPath`
performs keeps the user's notes in the file and leaves it loadable. Mutations — stripping nothing, and
stripping only the top level — fail these tests, and the first reproduces the original error verbatim.

The rejected alternative was to leave the loader alone and move `agents.$comment` up to a struct level: a
smaller diff, but it repairs one file rather than the convention, and a user who copies the example and
adds a note beside their own `agents` would still lose the whole configuration.

### The default model name is the one DeepSeek publishes

`config.DefaultConfig()` sent `deepseek-v4-flash`, which DeepSeek's pricing page footnotes as a **retired**
name: still accepted, served by DeepSeek-V4.1-Flash, billed at the Flash price. Nothing was broken by it —
which is why this was a chore and not a bug — but the model name is also the key a declared rate is looked
up by (`<route>/<model>`), so a retired alias is a second name for one route that can drift, and a price
table naming only one of the two silently fails to price the other. The default, the shipped example and
the example's price key now all name `deepseek-flash` (issue #226); the alias keeps working for a user who
typed it into their own config.

`TestDefaultConfigSendsTheCanonicalDeepSeekModel` asserts the name in the **request body** a default
configuration actually builds — not only in the struct field, which is what a provider may still override —
so the assertion covers the path `DefaultConfig` → `buildProviderRegistry` → the wire. `config_test.go`'s
check that `AddAllowedPath` never materialises defaults now names the model the defaults carry today;
naming the retired alias would have kept passing while looking for the wrong string.

The example's declared DeepSeek rates are its **peak** rates, and the `pricing` block's `$comment` now says
so: one price per lane cannot express the off-peak half, so a declared estimate is 2× too high for about
79% of the week until the schema can hold both
([#225](https://github.com/yusiwen/TinyCode/issues/225)). The note has to sit at that level rather than
beside the rates, because `prices` is a map and a `$comment` inside it is read as data — the same defect
that already keeps the whole shipped example from loading, filed as
[#229](https://github.com/yusiwen/TinyCode/issues/229) because it is pre-existing, it affects every section
of the file rather than this rename, and it leaves the choice of fix shape open.

### Model pricing: the decision not to ship a price table

[docs/pricing-decision.md](docs/pricing-decision.md) records what this repository will and will not do
about knowing what a call cost (issue #182). The verdict ships **no vendored price table** and leaves that
question open on a named condition: the route's own reported charge (OpenRouter states `usage.cost` per
request) and rates the user declares stay the only two mechanisms, a call neither can price is counted as
unknown rather than as zero, and a table cannot be right for our own default route until the schema can
hold what it would say ([#225](https://github.com/yusiwen/TinyCode/issues/225)).

The measurement behind that is that the two leading open datasets disagree about this repository's own
default route. For `deepseek-flash` input, LiteLLM's base entry says $0.30/M and models.dev says
$0.15/M — and both are right, because LiteLLM carries DeepSeek's peak rate with an `off_peak_pricing`
block holding the other half, while models.dev carries the off-peak rate as its only number. A vendored
single value is therefore ambiguous rather than merely stale: which one is correct depends on the hour.
DeepSeek bills peak at 01:00–04:00 and 06:00–10:00 UTC, Monday to Friday, which is 20.8% of the week.

The same measurement exposes what has to happen instead. `agent.Price` has five lanes and no time
dimension, so the user's *own* declaration cannot be right either: `config.example.json` declares the
peak rates, which makes its estimate 2× too high for about 79% of the week with nothing saying so. That
schema work is [#225](https://github.com/yusiwen/TinyCode/issues/225), and the retired legacy model name
the default still sends is [#226](https://github.com/yusiwen/TinyCode/issues/226). `docs/roadmap.md` A3
now points at the verdict rather than at the open question.

No application code lands from #182; this document and those two issues are its deliverable.

### The tool registry lists the tools that exist

`CODEBASE.md`'s table of built-in tools is titled "Complete Tool Name Registry" and is written by hand.
It was not complete: `web_extract_browser` is registered in `main.go` and the table had no row for it —
an omission its row count hid, because the table has 24 rows once the dynamic `mcp_*` row is counted as
one of the tools, so the number a reader checks agreed with the documented "24 tools" while one real
tool was missing (issue #188). The table is complete again, and
`TestDocumentedToolRegistryMatchesTheCode` now reads both sides — the `Name: "…"` literals in `tool/`
plus the four `lsp` tool types, against that table's rows — so a tool the package gains without a row,
or a row without a tool behind it, fails the build.

The other half of the issue was a tool that never existed. `memory` was listed among the tools in the
README's System Overview and, long after #187's figure replaced that diagram, still sat in the TUI's
housekeeping whitelist as `{"todo": true, "memory": true}` — a name no registry has ever held. It is not
a tool: `agent/memory.go` implements `HTTPMemoryClient` against a remote service and nothing constructs
it, `Agent.Memory` is never assigned outside its own declaration, and recall is an agent-layer step
(`MemoryModeAuto`) that no caller enables. `CODEBASE.md` now says so where it describes the client and
the `MemoryStore` fields, the whitelist holds only `todo`, and `docs/roadmap.md` records the decision —
`memory` is a capability waiting for a caller (B1/B2, on a privacy decision), not a tool waiting to be
written.

Two stale line references in that roadmap entry were corrected in the same pass: `Agent.Memory`'s read
is at `agent/agent.go:264-265`, not 148-149, and the whitelist is at `tui/update.go:578`, not 536.

### Nested agent worktrees are ignored by git

An agent session keeps its scratch worktree inside the workspace root (`.worktrees/<scope>`) because
the session's file policy may allow writes only there. Nothing ignored those directories: after
`git worktree add .worktrees/probe …` the outer tree reported `?? .worktrees/`, and a plain
`git add -A` there staged the worktree as an *embedded repository* — a gitlink in place of the files,
which git warns about but records anyway in a commit that is easy to miss (issue #167).

`.worktrees/` is now in `.gitignore`, so a worktree created there is invisible to the outer tree's
status and staging, and `git worktree remove` is the cleanup path without a second step.

### The command palette and provider picker fit a narrow terminal

The input area's rows are drawn below the cell grid like the dialog's, so a row wider than the window is
cut by the terminal itself. The command palette's rows are `  <name>  <description>`; the widest is 36
cells, so a 30-column terminal showed `Toggle thinking display` as `Toggle thinking` and lost the rest.
The provider picker's labels are built from the user's own provider names —
`openrouter-anthropic-claude-sonnet (mock)` is 41 cells — so the picker could lose the end of the very
name the user is choosing (issue #221).

Both are now budgeted against the width. A description that fits beside its name in full stays there; one
that does not takes a line of its own under the row, where it gets the whole width, because splitting it
into the narrow column beside the name leaves fragments like `LSP` / `diagnostics`. A picker label is
wrapped like the dialog's heading, with the selected row's header style budgeted with it, and the row
marker stays on the first line so a wrapped label continues under its own text. The `Commands:` and
`Select provider:` headings are budgeted too — the second is 16 cells before its padding, wider than a
narrow terminal. A long token with no space in it is now split after a hyphen, slash or dot where one
fits (`openrouter-anthropic-` / `claude-sonnet`) and at the cell limit only when there is none
(`openrouter-anthropic-claud` / `e-sonnet`).

The suite gained a `provider` frame set (80x24 and 30x12) and a 30x12 palette, both with narrow
screenshots; no committed frame changes above 30 columns.

### The permission dialog fits a narrow terminal

The input area is drawn below the cell grid, so it is never trimmed against the terminal: a line wider
than the window is cut by the terminal itself, mid-word and without a mark. At 40 columns the permission
dialog's hint row was 54 cells — `↑↓ navigate · Enter select · 1-9 shortcut · Esc cancel` — and the user
read a sentence with no end, `Esc cancel` being the part that went missing; the heading was 47 cells, and
the always-allow answer names the config file it writes, so it is 60 cells before its `[3] ` marker is
added (issue #218).

The three hints are now lists of alternatives rather than finished strings, and the input area lays
itself out for the width it has: the heading and the answers wrap at word boundaries, an alternative is
never dropped to make room, and a wrapped hint line ends with its `·` so it still reads as one list — at
40 columns the shortcut row becomes two lines and `Esc cancel` survives. A word that cannot fit a line
even alone is split across lines rather than clipped, because the always-allow answer names the file a
permanent permission will be written to and a reader cannot consent to a path they can only half see. An
unknown terminal width still draws every line in full, as before.

The committed frames follow: `dialog_40x12` is regenerated, no committed frame is wider than the geometry
it claims any more, and `TestGoldenFrames` now asserts that per frame instead of requiring one frame to
overrun. The clip keeps an over-wide fixture of its own, since borrowing a screen that happens to
overflow would mean keeping a defect to test the clip.

### A long status bar keeps its status message

The status bar is one line: the session facts, then `│ <status message>`. The model rendered it in full
and the **terminal** clipped it at the window width, so as the segments grew — a budget fraction
(#179), a reported charge (#181) — the message at the end was what got cut. At 120 columns
`Press Ctrl+C again to quit` arrived as `Press Ctrl+C again`, and that phrase is the only signal that a
first Ctrl+C was taken as a quit rather than as an interrupt (issue #196).

`fitStatusBar` now budgets the line before it is rendered: the message is measured first and kept whole,
the remaining segments are added left to right while they fit, and a segment that does not fit is left
out **whole** — a segment cut in half can be misread (`0.95 credits` as `0.95 credit`, a fraction losing
its limit) where an absent one cannot. When no width can make the message fit, it is clipped with a
visible ellipsis rather than silently truncated. An unknown terminal width still builds the bar in full,
as before.

This also makes the committed frames honest for the status bar: they used to carry the same
126-character line at every geometry, because the *test* clipped and the model did not, so the goldens
pinned the text while the screen showed a cut sentence.

`status-cost.scenario` comes back from 200x50 to **120x30** — the width at which it used to fail — and
its exit sequence, which waits for the whole confirmation, is the assertion that the message survives.

### Resizing a running program is covered again

The TUI's in-process program-driver test was deleted when the harness moved into the `tuiprobe`
module, and it was the only thing in the repository that resized a *running* program: the scenario
runner had no `resize` step, so nothing drove a terminal resize into the real binary (issue #214).
`tui/geometry_test.go` still covered the model's answer to a `tea.WindowSizeMsg`, which made the
loss a narrowed property rather than an open hole — but the terminal's half was untested.

`tuiprobe v0.1.5` adds the step (the pty, session, daemon and CLI layers could all resize already;
only a scenario file could not reach them), and `tui/testdata/scenarios/resize-running.scenario` is
the guard: it opens the binary at 80x24, `mark`s the screen, resizes to 40x12, and then waits for the
input row with `--since` — text that can only match if the program redrew after the resize — followed
by `fit --size 40x12` and a screenshot whose pixel size is asserted against the new geometry. Deleting
the `resize` line makes the scenario fail, which is the control that keeps the assertion meaningful.

The `tuiprobe` pin moves to `v0.1.5`.

### A cold toolchain can fetch inside the boundary

`confine_commands` granted one root beyond the project — the platform user cache
directory — which is enough for `go build` and `go test` on a warm checkout and not
enough for a cold one: a module that is not already in the module cache could not be
downloaded by a confined command, because `GOMODCACHE` (`~/go/pkg/mod`) was not a
granted root and a confined command cannot create a directory it was never granted
(issue #166).

The granted set is now the user cache directory plus the download cache of every
toolchain installed on the host (Go's module cache, cargo's registry, npm's cache,
pnpm's store — each honouring its own environment variable). Two rules keep the
widening defensible, and both are tested:

- **A cache subtree only, never a directory on `PATH`.** `GOPATH` holds `go/bin`
  beside `go/pkg/mod` and `CARGO_HOME` holds `bin` beside `registry`; granting either
  home would let a confined command leave a program behind that the user later runs
  outside the boundary — the sandbox escaping by persistence. `PNPM_HOME` is refused
  for the same reason.
- **Only an installed toolchain, and only a cache that exists.** The second half is
  not pedantry: the Linux applier refuses to run with a root it cannot open, so
  granting a phantom path would break every confined command rather than widen the
  boundary. The composition therefore creates the installed toolchains' caches at
  startup, before it reads the roots — which is exactly what a cold checkout needs.

The set still lands through the one shared list (`WritableRoots`), so the file fence
and the command boundary cannot drift, and the drift guard stays green. `/sandbox`
now states the cache policy and the key that grants one more path, and
`docs/sandbox.md` names the whole set, both rules, and where to add a toolchain this
list does not know.

### A long generation is no longer cut, and the idle bound can fire

Two defects with one root cause: `agent/provider_openai.go` armed a single
`http.Client{Timeout: openAIRequestTimeout}` for both request shapes. Go's
`http.Client.Timeout` covers *reading the body*, so on a streaming call it was a total limit on the
generation — the opposite of what the file's own comment promises. Measured against a server delivering
a line every 30 ms: a healthy 300 ms answer was cut at 102 ms with `Client.Timeout or context
cancellation while reading body` (#185).

The same timer made the idle bound unreachable at the shipped defaults: both bounds are 120 s, and a
client timer starts at the request while the idle watchdog rebases on every line, so once a line had
arrived the client timer always won. The idle bound's diagnosis was therefore correct only in the tests,
which shrank the idle side and left the request side at 120 s (#186).

Both are fixed by taking the timer off the client. The streaming path's only bound is the idle
watchdog, which also covers the wait for response headers. The batch path — no interim progress to
watch — gets a watchdog on the same request context, naming its bound the way the Ollama provider does
(`no answer within 120s`). The transport keeps its own phase limits (dial, TLS) from the default
transport, and no bound is armed on the client itself, so neither defect can be reintroduced by
lowering a client timer.

### TUI verification moved to `tuiprobe`

The TUI visual harness's mechanism — terminal emulator, PTY scaffolding, golden
helpers, SGR→CSS renderer and Chromium plumbing, about 1,400 lines — now lives in the
nested `tuiprobe` module, released as `tuiprobe/v0.1.0` and pinned in `go.mod`.
`tui/` keeps the fixtures and the judgments: one scenario table (ten builders) plus
assertions that are a few lines each, in `tui/scenarios_test.go` and
`tui/verification_test.go`.

Proven before deleting: the 27 committed text goldens and the ANSI golden are
reproduced **byte for byte** through the same builders, and both suites ran green in the
same CI job. See [docs/tui-verification.md](docs/tui-verification.md) and
[tuiprobe/docs/parity.md](tuiprobe/docs/parity.md).

### Provider-reported token usage in the status bar

The status bar's `tokens:` counter now shows the number the provider reported, not
`len(text)/4` over the streamed answer. The request already asked every
OpenAI-compatible endpoint for usage (`stream_options: {include_usage: true}`), but the
usage-only chunk that answers it was thrown away twice over: the SSE parser skipped every
chunk without choices, and it stopped reading at `finish_reason`, which arrives *before*
that chunk. Ollama's `prompt_eval_count`/`eval_count` were dropped the same way.

The reported numbers now reach `Agent.UsageTotal` and the counter, which falls back to the
estimate for an endpoint that reports nothing. Because usage covers the prompt too,
`tokens:` is larger than it used to be — the estimate counted streamed output only. Roadmap
entry A2; cost and pricing accounting remain open with A3.

### A stream that goes quiet now fails instead of hanging

Reading to `data: [DONE]` is what made the usage chunk reachable, and it took away the early
return at `finish_reason` without putting anything in its place: an endpoint that sent a
complete answer and then held the connection open left the step waiting for the provider's
whole 120 s request timeout, and the text it had was handed back as a **successful** answer.
The OpenAI-compatible provider now carries the same pair of bounds the Ollama one does — a
whole-request bound for a batch call, and a per-line idle bound (2 min) for a stream — and a
read that is cut returns an error naming the bound instead of a partial answer. A caller
cancellation is still recognisable as one, so an interrupted run keeps reading as
interrupted. Reported as #176.

### A run can be stopped at a token budget

`budget.max_tokens_per_run` and `budget.max_tokens_per_session` bound what one prompt's ReAct loop and
a whole session may spend, counted in the tokens the provider reports. Both default to `0` =
unlimited, so a configuration that does not set them behaves exactly as before.

The loop checks before every provider call — the only point that spends — and a run that reaches
either limit ends with a message naming the limit, the spend and the knob to raise, instead of making
one call too many. A provider that reports no usage is counted as an estimate of its output, so a
silent endpoint cannot switch the budget off; such a call never fires the usage event, which stays a
report of what the provider actually said. Sub-agents enforce the same limits against their own
spend — attributing a sub-agent's tokens to its parent is roadmap G1. Roadmap entry A3.

### Cost accounting: what a call charged, and what it cost

Two things were missing to answer "what did this session cost", and they are separate problems.

**What a route charges.** Some routes report it per request: OpenRouter returns a `cost` in its usage
object, and a `prompt_tokens_details` split of cached and cache-written input beside it. Those numbers
are now carried through unchanged — never recomputed, never added to a figure derived from a price
list — with `cost_currency` on the provider naming the unit that route bills in.

**What the tokens were.** A route that does not report a charge has to be priced from rates, and rates
need the lanes: DeepSeek bills four input lanes (cache hit vs cache miss) times a peak/off-peak rule,
which a single input price cannot express. `Usage` now carries `cached_prompt_tokens`,
`cache_write_tokens` and `reasoning_tokens` — one field per billing lane, so OpenAI's nested
`prompt_tokens_details` and DeepSeek's flat `prompt_cache_hit_tokens` land in the same place.

`pricing` in the configuration then declares rates per million tokens, keyed by `<route>/<model>` with
`*` wildcards, because a price belongs to a billing endpoint rather than to a model name. A call is
priced from a declaration only when its tokens were *reported*: multiplying a declared rate by a
guessed token count would dress a second estimate as a measurement. Everything else is counted as
**unknown**, and never folded into a total as zero — `Ollama` and subscription plans have no per-token
price at all, and "0" would read as free in exactly the decisions the number is there to inform. The
status bar shows money only once cost accounting is in play (a rate declared, or a charge reported),
and then shows the count of calls the total does not include beside it; a session with neither keeps
the bar it has always had. Whether this project should ship a price table of its own is a separate,
undecided question (#182).

### Transient provider failures are retried

A rate limit, a model still loading or a connection reset used to end the step. Both providers now
retry a failure that a second attempt could fix — 408, 429 and any 5xx, plus a transport error that
produced no response — with exponential backoff, equal jitter, and a floor at the `Retry-After` an
endpoint sends (its HTTP-date form is ignored rather than mis-parsed). The pause is capped, and the
call is bounded by the caller's own context, so Ctrl+C during a backoff stops the sequence instead of
being noticed after the next request.

The rule that keeps this honest is the **safe-replay** rule: a streaming attempt is repeated only when
nothing has reached the caller yet. Text already drawn cannot be drawn twice, and a duplicated answer
is worse than the failure, which at least says what happened. A batch call has nothing on screen, so it
is safe to repeat whole. A retry announces itself (`retrying (2/3): …` in the status bar, a line in
one-shot mode), because a pause with nothing on screen is indistinguishable from a hang, and a final
failure names how many attempts it took.

Not retried, deliberately: a context error, a body we cannot parse, and any other 4xx — a rejected key
or a missing model answers the same way however many times it is asked.

### A long task announces itself when it finishes

A background `task` used to end in silence — the user found out by asking `task_collect` or by noticing
the interface. A finished background task now announces itself, and so does a foreground run that
outlived a threshold.

- **terminal** (the default) writes `OSC 9` and `OSC 777`, which iTerm2, WezTerm, kitty, Ghostty and
  rxvt-unicode render as notifications; a terminal that knows neither ignores them, so this is safe to
  have on. **desktop** (opt-in: `channel: desktop` or `both`) runs the platform's notifier —
  `osascript` on macOS, `notify-send` on Linux — with the text passed as arguments rather than
  interpolated into a script, so a goal containing a quote cannot change what runs.
- `notify.min_duration_seconds` (default **30**) gates everything, so a two-second task stays quiet;
  `0` announces every completion. A threshold rather than a focus check, because there is no portable
  way to ask a terminal whether it has focus. `channel: off` turns the feature off.
- Nothing makes a sound, and there is no Windows notifier (`channel: desktop` is macOS/Linux). A
  machine without the binary is silent rather than noisy: the run it was announcing has already
  finished.
- The announcement text is sanitized: a title or body containing ESC, BEL or a line break has those
  bytes stripped, because that is how a task's own goal text could otherwise end the escape and write
  to the terminal.

Roadmap entry D5.

### A tool call is checked before it runs, and a loop is broken

A model's tool call went straight from "the JSON parsed" to the tool. A missing required field, a wrong
type or a value outside an enum reached `Execute` and came back as whatever that tool's own parsing
happened to say — or, worse, as a result that looked fine for a call that did something else.

Arguments are now validated against the same schema the model was shown, and a call that breaks it is
**not executed**: the model is told every problem with it at once (field, what the schema wants, what
arrived), so one step can fix three typos instead of three steps fixing one each. The validator
implements exactly the keywords the 24 built-in schemas use — `type`, `properties`, `required`, `enum`,
`items`, counted by a command rather than guessed — and ignores every other keyword, because refusing an
argument over a rule it cannot evaluate would be worse than not checking it.

A model that repeats the *same failing call* is now refused instead of being handed the same failure
until the step budget runs out: the call's identity is its tool and its arguments (key order does not
matter), it is counted per run, and after `repeatFailureLimit` failures the answer says so and says to
change the approach. A repeated **successful** call is never counted — polling a build is work, not a
loop — and a fresh prompt is a fresh start.

Roadmap entry F3.

## v0.0.7 — 2026-10-01

### Language server integration

- LSP documents are synced with their real text (`didOpen` with content, then
  `didChange`) instead of being registered as empty buffers, and `Init` rebinds
  the server when the workspace root changes, so diagnostics are no longer
  "not included in your workspace" noise. (`d2cf66e`)
- The server is chosen from the project language, falling back to the touched
  file's own extension; a file with no configured server now fails fast instead
  of being handed to `gopls`.
- The LSP integration tests run in CI against a pinned `gopls` (0.23.0) through
  `make test-lsp`, and the mock server answers navigation requests so the
  go-to-definition, references, hover and document-symbol paths are covered.

### Security and sandbox

- Linux path containment is checked by the kernel (`openat2` with
  `RESOLVE_BENEATH`), including through symlinks, without rejecting in-root
  symlinks. (`e947654`, `da2f077`)
- The SSRF policy is shared with the MCP HTTP transport, the browser's
  top-level host is pinned to the validated IP, and Chromium requests are
  intercepted so a redirect cannot bypass the check. (`6031cf5`, `5122376`,
  `dfbbe16`, `7f3db7b`)
- The loopback exemption is scoped to the configured authority. (`3cc21f4`)
- A sandbox denial is refused immediately when no permission dialog exists,
  instead of waiting for a prompt that can never appear. (`ddf483e`)

### Reliability

- MCP requests are correlated by id with per-call cancellation, and each
  request now carries its own skip budget: one starved call fails on its own
  instead of aborting every call in flight. (`c97447b`)
- Server-initiated MCP requests are answered so the peer is never left waiting,
  and client metadata is locked. (`97aba4b`)

### Tooling

- CI additionally enforces `gofmt`, the race detector, repeated runs
  (`-count=3`), a cross-compile/vet matrix and a blocking, pinned
  `staticcheck`. (`0bf8a1c`, `abe33ed`, `5b8df7d`, `b50ec21`)
- Generated and exported output is written with private permissions. (`78f4d9e`)

### Browser sandbox

- The headless browser now runs behind a loopback filtering proxy
  (`internal/browserproxy`). Chromium resolves DNS, follows redirects and loads
  subresources itself, so the previous pre-flight check and the rod request
  interceptor could not cover the `--dump-dom` exec path, and a redirect or
  subresource hostname was checked by name but resolved again by Chromium. Every
  connection now asks the proxy, which resolves each hostname once, validates
  every address against the shared SSRF policy and dials only a validated,
  pinned address; private, loopback and metadata targets are refused with 403.
  Redirects are handed back to the browser (so the next hop is validated too) and
  QUIC is disabled because HTTP/3 would leave over UDP without asking the proxy.
  Loopback does not bypass the proxy (`--proxy-bypass-list=<-loopback>`), and the
  rod path keeps the request interceptor and the top-level host pin as fallbacks.
- Policy refusals from `internal/netsafe` now wrap a sentinel (`ErrBlocked`) so
  a caller can answer 403 for a blocked target and 502 for an unreachable one.
- `bash` timeout now reaps a descendant that escaped into its own session with
  `setsid(2)`: the process tree is collected and killed before the process group,
  using the kernel's process table on darwin and `/proc` on linux (no `ps`
  subprocess). A double-forked descendant that re-parents to init before the walk
  remains out of reach, as documented.

### Browser detection and smoke test

- `findPlaywrightBrowser` now recognises the layouts Playwright actually ships.
  It only knew `chromium-<rev>/chrome-mac/Chromium.app`, so a current
  installation (Chrome for Testing under `chrome-mac-arm64`, or a
  `chrome_headless_shell` package) was invisible and the browser tool silently
  downloaded its own copy instead. Revisions are compared numerically (999 does
  not outrank 1000), the full browser is preferred over the headless shell, and
  the rod path hands the detected binary to its launcher.
- `make test-browser` (`BROWSER_TEST=1`) drives both browser paths against a
  loopback page that only shows its text after JavaScript runs, and asserts the
  request reached the server through the filtering proxy (the proxy's `Via`
  header). A `browser` CI job runs it with Chrome for Testing; the test skips
  without a browser, so ordinary test runs never launch one.

### Sandbox

- File I/O in `read_file`, `write_file`, `edit` and `apply_patch` now opens
  through the sandbox root with `openat2(RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS)`
  on Linux, so the kernel decides containment on the same descriptor that is read
  or written and the documented check-then-open window is closed for in-root
  paths. Paths allowed outside the root, other platforms and kernels older than
  5.6 keep the previous plain-open behaviour, so the layer only ever adds
  enforcement.

### Testing and verification

- Fuzz targets now cover the fuzzy edit matching (a match must be deterministic
  and its range must hold the text it claims), indentation correction, the
  Levenshtein metric, the sandbox `relBeneath` helper, the TUI's word wrapping
  and markdown parser, the MCP `Content-Length` frame reader and the SSRF IP
  policy. `make fuzz` runs all of them (`FUZZTIME=30s` by default); their seed
  corpora run with every `make test`.
- Fuzzing found and fixed a silent file-corruption bug: a search holding a lone
  UTF-8 lead byte matched the first byte of a multi-byte character, so the edit
  replaced one third of it. It also found that `normalizeAuthority` was not
  idempotent for authorities with an unmatched `]`, and that
  `agent.ToolAllowedFor` panicked on a nil config.
- Coverage at the release: types 100%, skill 91.8%, tlog 91.7%, agent 90.2%,
  browserproxy 90.1%, session 88.9%, mcp 84.0%, tui 81.6%, root 81.0%, config 80.9%,
  tool 79.3%, netsafe 78.7%, lsp 74.2% (710 test functions, 10 fuzz targets, measured
  with `GOPROXY=off go test -count=1 -cover ./...`). The README badge carries the test
  count and `CODEBASE.md` lists every count together with the command that produces
  it, badge included.

### Release and tooling

- `release.yml` moved to Go 1.27: the `go.mod` directive now requires it, so the
  previous 1.24 pin would have failed the release pipeline.

### Bounded work and deadlines (2026-10-01)

- The Ollama provider no longer trusts a stalled endpoint: a request deadline plus an
  idle watchdog that resets per streamed line, so a server that stops answering
  mid-stream fails with a named error instead of hanging the run. (`fd45f67`, #1)
- `tools/call` gets a default deadline when its caller supplied none, and a response
  that arrives just as the stream closes is delivered rather than reported as
  "reader stopped". (`d632a6a`, `617b5b2`, #2, #39)
- A browser probe that ran out of its budget is no longer cached as permanently
  unusable; the verdict expires after 30 s and is retried. (`a2adc87`, #26)

### File and process safety

- `apply_patch` checks the read its apply pass depends on, so a stale or unreadable
  target fails the patch instead of truncating the file. (`bbd1b37`, #6)
- `writeSandboxed` replaces files atomically: the bytes go into a temp file in the
  target's own directory through the sandbox-aware open, one rename moves it over the
  target, the existing mode is preserved and the temp is removed on every failure
  path. An interrupted write can no longer leave an empty or half-written file.
  (`e9a2935`, #36)
- Where `openat2` cannot reach — a path allowed outside the root, macOS, kernels older
  than 5.6 — the resolved path is walked one component at a time with `O_NOFOLLOW` and
  re-resolved before the open, so a component swapped for a symlink is refused
  (`ELOOP`, `errPathChanged`) instead of followed. (`dd5bd27`, #7 S1)
- The language-server tools hand the server the bytes they actually read instead of
  re-opening the path, closing the window between the sandbox check and the server's
  own read. (`3fa7a1d`, #7 S2)
- On Linux, bash commands run inside a cgroup: a descendant that double-forks and
  escapes the process group is killed with the cgroup when the command times out, so
  it cannot outlive the tool call. A portable test proves the escape still happens
  without the cgroup, which is what keeps the Linux-only path justified. (`9294296`,
  #9)

### Browser sandbox

- The headless browser disables QUIC, WebRTC's non-proxied UDP and background
  networking, and refuses to start without the filtering proxy instead of falling back
  to a flags-less launch. (`9b4462f`, #8)
- The `--dump-dom` path prefers the Playwright headless shell: on macOS the full
  desktop build produced no DOM at all (nothing in 120 s) while the shell from the
  same revision answered in about a second. The rod path keeps the full browser and an
  explicit `CHROME_PATH` still wins over both. (`a5f7bfc`, #43)

### Coverage, tests and the second language server

- The search fallbacks (`rg` → `grep` → Go walk), the HTTP MCP transport and the
  filtering proxy are covered, and the proxy gained a fuzz target asserting that a
  refused target never reaches the dialer. The search ladder now falls through to the
  portable implementation when an installed tool fails, instead of failing the whole
  search. (`f367f37`, #4)
- A second language server is exercised end to end, which immediately found a real
  bug: the client advertised no capabilities at all, and tsserver gates every
  diagnostic on `textDocument.publishDiagnostics`, so every TypeScript file looked
  clean while gopls pushes regardless. (`7a9e587`, #10)
- The dead `no_ollama` build tag was removed; it broke `go build -tags no_ollama`.
  (`54eeb1f`, #5)

### Documentation and discipline

- The TUI visual harness has a reference (`docs/tui-visual-harness.md`), the harness
  and verification rules it produced are recorded in `AGENTS.md`/`CODEBASE.md`, and
  the gated checks document how to run them locally. (`353872f`, `66348df`, `ebb5d60`,
  #11)
- The test-count badge is back inside the measurement discipline: the README badge
  said 564 while `CODEBASE.md` said 667, both wrong. Every count now names the command
  that produces it, the badge included. (`ebb5d60`, #32)


## Historical feature log

The list below is the project's feature log up to the point where it moved out
of `README.md`. All planned features of that phase are implemented; the MCP
client supersedes the original plugin-system proposal.

- [x] **Incremental CellGrid Rendering** — msgDirty tracking, View() skips unchanged messages. Benchmark: ~2.3ms regardless of session size. (ffbbf22)
- [x] **Agent-level unit testing framework** — 13 integration tests using MockLLM step-by-step. (a3868e2)
- [x] **Multi-agent session tree** — `/fork` + `/session` branching conversations. (93f1665)
- [x] **Theming** — default + nord palettes, `/theme` command, persists to config.json. (e4bcf85, 533d167)
- [x] **Session management** — delete, export Markdown, search via CLI flags. (2236e84)
- [x] **LSP** — long-lived connection, background diagnostics, mock test framework, incremental diagnostics (SnapshotBaseline+GetNewDiagnostics), TUI error tracking (LSPDiagMsg, status bar "errors: N", /diagnostics command). (2ab4338, ace09ff, a2e3e07, 290818a)
- [x] **GitHub Actions CI/CD + Makefile improvements** — main.yml (build+lint+test), release.yml (cross-compile+release), Makefile test/releases targets. (ab07697, bddeed5)
- [x] **Skills & Subagents** — SKILL.md-based discovery + /skill command + 2 builtin skills. 3 new subagents: general (parallel research), compact (history compression), title (session naming). /explore command removed (explore kept as subagent). (cbd6db3, 8fa8800, adfa51b, c0b8ae8)
- [x] **Todo Feature — P0+P1+P2 Complete** — TodoStore + todo tool + JSON Schema (P0), TUI rendering with [x][>][ ][~] markers (P1), compression protection + housekeeping mute + session recovery (P2). 21 new tests. (2f51d06, 94db0e3, 25caefc)
- [x] **Todo TUI display fixes** — Always render TODO between reasoning and tool calls (not gated by todoDirty). Hide `todo` from tool call list. Persist across CellGrid rebuilds. Add blank line separator between TODO and `→ Calling tools:`. Render-acknowledge gate for concurrency safety. (720c8b6, 12ae5db, 01410a9)
- [x] **Line-Level Code Edit — edit + apply_patch** — Search/replace edit tool with 7 fuzzy strategies + indentation correction. V4A multi-file patch format. 23 new tests. (d067156, 9045176, 34d2c17)
- [x] **Title Agent & Session Titles** — title hidden agent generates conversation titles via LLM after first exchange. Applied on session save. (2deee5e)
- [x] **Edit Fuzzy Matching** — 7 fallback strategies (line-trimmed, ws-normalized, indent-flexible, escape-normalized, unicode-normalized, block-anchor). Indentation correction. 6 new tests. (34d2c17)
- [x] **Web Tools Phase 1-3** — web_search (DuckDuckGo Lite + SearXNG), web_extract (HTTP→CF→Cache→Wayback→Chromium, SSRF, LLM summary). 26 new tests. 21 tools total. (5c90f86, 4cb7ce1, 4bf27e5)
- [x] **SearXNG Config** — `searxng_url` field in config.json, wired via SetSearXNG(). (405bc87)
- [x] **MCP Client (4 Phases)** — stdio/HTTP transports, auto-discovery, agent.Tool registration, resources, SSRF security. 22 tests. 359 total. (e31c08b, cc1ba8e, 7b4fe75)
- [x] **Permissions engine (Ruleset)** — Replaced DeniedTools/AllowedTools with `{action, resource, effect}` Ruleset. Last-match-wins evaluation. Whitelist mode (`*: deny` + specific allows). FilterTools for sub-agent creation. (378a0fd)
- [x] **Async task + task_collect** — Background task manager for parallel sub-agent execution. `task({..., bg:true})` returns task_id immediately. `task_collect({id})` waits for completion. (dd1688f)
- [x] **Concurrent tool execution** — Multiple tool calls in the same step run concurrently via goroutines + result channel. Agent waits for all to complete before next step. (e5fd457)
- [x] **Sub-agent sandbox propagation** — `PermissionRequest.AgentLabel` tracks which agent requested path access. TUI dialog shows `[general] Write to /path?`. `SetAgentLabel()` called before sub-agent creation. (d0653ee)
- [x] **Prompt improvements** — Build mode system prompt guides LLM to use `task()` for parallel delegation, use relative paths, and separate paragraphs with blank lines. Explore agent has dedicated PROMPT_EXPLORE-style prompt. (effbe2c, a02d894, f66f151)
- [x] **Command palette UX fix** — Selecting a command from palette fills the input box. User presses Enter again to execute. No more immediate execution + stale text. (118e938)
- [x] **Status bar processing indicator** — Animated dot spinner + ● fallback indicator during streaming. Shows when agent is actively processing. (6adfda0)
- [x] **Mouse selection clamp** — `posFromCoord` clamps to last valid row instead of returning `Offset: -1` when dragging past content end. 5 new tests. (39e80e5)
- [x] **Multi-message step split** — Each agent step creates its own assistant message with independent reasoning, tool calls, TODO snapshot. `OnStepDone` callback fires between steps. (c5f854f, 4ef98ff)
- [x] **TODO snapshot per message** — Each assistant message carries the TODO list state at creation time. Resume restores TODO from history. Todo render ack fixed — ackCh moved outside TODO injection block to prevent agent deadlock. (e34143c, 633a641)
- [x] **Remove redundant UI** — `⏳ Running...` indicator removed (spinner in status bar is sufficient). `(processing...)` text in input area removed. `Response:` label removed (message color differentiates roles). (4e832f1, b35bdd7)
- [x] **[Copy] button fixes** — Button row counted in `msgRowCount` so next message doesn't overlap. `activeButtons` preserved per `MsgIdx` across renders so button stays clickable after incremental rebuild. (f9143cc, 8dffd64)
- [x] **Spinner pipeline fix** — Tick kept alive even during idle. `StatusStreaming` preserved across intermediate steps so spinner doesn't stop mid-task. Switched to braille `Dot` type (user terminal supports it). (9a0f55b, afc14e1, 42b8794)
- [x] **Tab mode switch** — Tab now always switches plan/build mode (removed empty-input guard). (c75d9ae)
- [x] **Ctrl+C copy fix** — Character-level copy requires non-zero selection range (click without drag no longer copies single char). Ctrl+C twice now quits. (ea6711c)
- [x] **Permission dialog** — 4 options (Allow once=true once, Allow session=session persisted, Always allow=config persisted, Deny). Dialog auto-shows on View() without keypress. Dialog dismissed after Deny (Resolved field). Alignment: `>` separate column, `[1]` `[2]` aligned. (3e6f306, 57359db, 792120f, e8cff4f, de01f54)
- [x] **read_file triggers dialog** — Previously returned text hint to LLM; now calls `RequestPermission()` like `write_file` does. (e2832e3)
- [x] **Session resume restores history** — `inputHistory` populated from loaded user messages. `historyPos` reset to -1 so Up goes to most recent entry. (444dd04, 2e6f9b3)
- [x] **Duplicate tool call fix** — `OnToolCall` fired twice per tool (main goroutine + execution goroutine). Removed duplicate from execution goroutine. (cc5a224)
