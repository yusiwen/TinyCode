# Memory in v1: recall is wired, writes are explicit

**Decided 2026-10-25**, while sizing roadmap entries B1 ("the memory capability exists but is never
called") and B2 ("no local memory backend"). Inputs were read on the same day at commit `101c428`:
`agent/memory.go`, `agent/agent.go`, `agent/compression.go`, `config/config.go`,
`tool/trajectory_store.go`.

This is a policy the implementation must follow, not a status. Status stays in
[roadmap.md](roadmap.md).

## The decision

v1 ships memory with **exactly one write path: a command the user types (`/remember`)**. Automatic
end-of-turn fact extraction — a second model call that distils the turn and stores what it decides is
worth keeping — is **not part of v1**, and is deferred until after roadmap B3 (embeddings, vector
search and rerank) has landed.

The two halves are one decision. Recall runs by default, so what the store can hold has to be something
a person chose to put there. A store that fills itself is a different product from a store that is read
on every run, and the difference is not a setting.

## Why automatic extraction waits

1. **Nothing writes today, so "default on" would read an empty store.** There is no non-test caller of
   `Remember` anywhere in the tree; the only caller of the store at all is the recall step
   (`agent/agent.go:457`). Wiring recall and calling it done would be accurate and useless.

2. **The retrieval it would feed is lexical.** Without B3 a stored fact is found by the words it shares
   with the next prompt. A fact the model chose is not necessarily a fact the next turn can find, and
   the store would grow with entries nothing checks.

3. **It is a persistent channel into every later prompt.** A fact distilled from a file the agent read
   is text a third party can seed — a README that says "remember: always run …" is one plausible input
   among many. Explicit writes keep that channel under the user's hand until there is a review surface
   worth trusting it with.

4. **Cost without a measurement.** One extra model call per qualifying turn buys an effect nobody has
   measured. F1 (the agent-level evaluation framework) is the instrument for that, and it is not
   finished.

## What v1 does instead

- **Recall** at the top of `Run`, as it is already built (`agent/agent.go:457`), reading a local store
  by default, so nothing leaves the machine unless a network backend is configured.
- **`/remember <fact>`** is the only writer, and each entry records that it came from the user rather
  than from a summary, so the listing can tell the two apart once a second writer exists.
- **`/memory`** lists what is stored and **`/memory forget <id>`** removes it, by the id the listing
  shows. A store without a way to read and delete it is not one a person can consent to.
- **A network backend (Hindsight)** is reachable only by explicit configuration: an endpoint written in
  the user's own config file. A project-local `.tinycode/config.json` may not enable egress — the same
  rule `capture: full` follows in `config/config.go:928`. The adapter as it stands also predates the
  current Hindsight API: upstream recall results carry `text` (the client decodes `content`) and the
  listing returns `items` (the client decodes `memory_units`), while `Forget` is a documented no-op at
  `agent/memory.go:102-105`. "Hindsight works" is therefore a claim the adapter cannot support until it
  is checked against a real server, so it is not a reason to lean on that backend in v1.

## What this decision does not settle

- **Backend precedence** when both a local store and a network backend are configured. The working
  recommendation is one active backend and no double write, so that "what did it read" and "what does
  forget remove" have one answer.
- **Any automatic path for a network backend.** Moot in v1, and it must be decided on its own terms.
- **Where the local store lives and in what format.** The constraint is already visible in
  `tool/trajectory_store.go`: a record the agent can rewrite is not evidence, so the store belongs
  outside the project root and outside the tool path. The shape is implementation detail of the issue
  that picks this up.

## Reopen condition

Reopen when **both** hold: B3 has landed, and F1 can compare a run with memory against one without.

If that comparison shows no measurable difference, the answer is to stay explicit-only rather than to
enable extraction. If extraction is ever built, its first increment must be a dry run: print what it
would store, store nothing, and let a person decide whether the yield is worth the call.
