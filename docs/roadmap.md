# Missing-capability roadmap

An inventory of capabilities TinyCode does not have, recorded once so it does not have to be
re-derived. **This is a record, not a commitment:** an entry becomes work only when it is picked
up, at which point it gets its own issue with numbered increments (`S1`, `S2`, …) so that a large
capability does not become several issues nobody can close. A picked-up entry carries a
**Picked up** line linking that issue; an entry without one has not been started. The tracker is
[issue #168](https://github.com/yusiwen/TinyCode/issues/168).

## Provenance and method

- **Source**: a third-party inventory ("TinyCode 功能缺口清单", dated 2026-10-09) listing 39 items
  against baseline `7e6b017`.
- **Added since the inventory**: D7 and the resume/history defect its prerequisite names were not in
  the source report. They were recorded 2026-10-19 from a capability-by-capability comparison with a
  harness that has persistent goals, and every reference in them was checked with the same rule as
  the rest — a counted search or an exit code, never a sample.
- **Re-checked**: every item was verified against the source with counted or exit-coded searches
  (`rg -c`, `rg -q`; exit 1 = no match, exit 2 = searched wrong) rather than by sampling. Nothing
  here rests on a truncated listing.
- **Outcome**: the source's baseline numbers and quoted excerpts hold up exactly, and 38 of its 39
  items describe a real gap. One is refuted, and several overstate a narrower gap; both are
  recorded below so the overstated version cannot be re-derived.
- **The repository's own rule applies**: a number written into a document must be computable by
  one command. The counts quoted here were produced that way, and the source report's effort
  estimates are marked as estimates because no command can produce them.

Baseline numbers, measured at `7e6b017`:

```bash
find . -name '*.go' -not -path './tuiprobe/*' | wc -l          # 233
find . -name '*_test.go' -not -path './tuiprobe/*' | wc -l     # 125
find . -name '*_test.go' -not -path './tuiprobe/*' -print0 \
  | xargs -0 grep -h '^func Test' | wc -l                      # 749 (848 including tuiprobe/99)
go test ./... -count=1 && go test ./... -count=1 -race         # both green, 13 packages
```

## How to read an entry

Each entry carries a code, the gap, the evidence as a file and line, and — where the source
report described a mechanism as absent while a narrower one exists — that mechanism, so it is not
built twice.

Every reference was checked against `7e6b017` (83 references, none missing, verified by a sweep
that was itself mutation-tested: a renamed file and an out-of-range line each make it fail). The
line numbers are pinned to that baseline and **will drift** as the code moves. The durable part of
an entry is its file and symbol; read a line number as a starting point, not as a fact about the
current tree. D7 is the exception: it was recorded later, so its references are pinned to the
2026-10-19 tree instead.

---

## A. Model access layer

### A1 — No retry, backoff or rate-limit handling for provider calls

- **Picked up**: [#192](https://github.com/yusiwen/TinyCode/issues/192) — S1 (the policy:
  classification, exponential backoff with jitter, `Retry-After`), S2 (the OpenAI-compatible provider
  retries a *safe* attempt), S3 (the Ollama provider gets the same policy), S4 (visibility, tests,
  docs). Picking it up also corrected the **Already present** line below.
- **Gap**: one 429 or 5xx, or a stream that breaks mid-response, fails the whole step. No
  exponential backoff with jitter, no classification of retryable statuses, no safe replay.
- **Evidence**: any non-200 becomes an error (`agent/provider_openai.go:142-145`,
  `agent/provider_ollama.go:204-206`) and the loop returns on a provider error
  (`agent/agent.go:238`). `rg -in 'retry|backoff' agent/ -g '!*_test.go'` → 3 hits, all comments.
- **Already present**: **corrected** — this line used to point at `tool/web_browser.go:297` and
  `tool/web_extract.go:44` as an existing retry/backoff idiom. Neither is one. The first is
  `browserProbeRetryAfter`, the cooldown that decides when a timed-out browser-*probe verdict*
  expires, and the second is a sentence inside the `web_extract` tool's own description ("Fallback
  chain: direct HTTP → Cloudflare retry → …"). The only retry-shaped loop in the repository is
  `tool/sandboxio.go:100`, which picks a different temp-file name after a collision. Nothing retried a
  failed operation, so A1 was new code with no existing mechanism to reuse — which is why the entry's
  estimate is the honest one and the correction matters.
- **Effort**: ~1–2 d (source report estimate, unverified).

### A2 — Provider-reported usage is discarded; no cost accounting

- **Picked up**: [#174](https://github.com/yusiwen/TinyCode/issues/174) — S1 (`types.Usage` and both
  providers), S2 (`Agent.UsageTotal` and one `OnUsage` event per reported call), S3 (the status bar
  shows the reported number and falls back to the estimate). Cost and pricing accounting are **not**
  part of it and stay here, with A3. The evidence below is the `7e6b017` baseline this entry was
  written against, so its `rg` exit code describes that tree rather than the current one.
- **Gap**: token usage the provider already returns is thrown away, so there is no real usage or
  cost figure, and nothing to accumulate per session.
- **Evidence**: the request already asks for it — `stream_options.include_usage = true`
  (`agent/provider_openai.go:113`) — but `types.ChatResponse` has no usage field
  (`types/types.go:56-60`), the SSE event struct has none, and the usage-only final chunk carries
  empty `choices` and is skipped. `rg -n '"usage"' --type go` → exit 1.
- **Already present**: the status bar does show `tokens: N` (`tui/view.go:428`), accumulated from
  `agent.EstimateTokens` over streamed text (`tui/update.go:482-496`). That counter is an estimate;
  extend it with real usage rather than adding a second one.
- **Effort**: ~0.5 d.

### A3 — No cumulative token or cost budget, no circuit breaker

- **Picked up**: [#179](https://github.com/yusiwen/TinyCode/issues/179) — S1 (the config surface),
  S2 (enforcement before every provider call, per run and per session), S3 (the status bar shows the
  session limit when one is set), S4 (tests and docs). The **token** half is delivered, and **cost**
  followed in [#181](https://github.com/yusiwen/TinyCode/issues/181) — a provider-reported charge
  carried verbatim, user-declared rates keyed by route and model, and the cache-tier usage detail that
  money has to be computed from, with a call nobody could price counted as unknown rather than as
  zero. Its price-table question was decided in
  [#182](https://github.com/yusiwen/TinyCode/issues/182) — **no vendored table**, with the rejected
  options and the verified inputs recorded in [pricing-decision.md](pricing-decision.md) — and the piece
  that verdict hands over is the schema's missing time dimension,
  [#225](https://github.com/yusiwen/TinyCode/issues/225). **Delivered 2026-10-18**: an entry carries
  optional windows (weekdays and UTC hours) whose rates replace its own, the shipped example declares
  DeepSeek's peak and off-peak pair, and a schedule that overlaps, covers the whole week or leaves a lane
  unpriced is a startup error naming the window. That split narrows this entry's original wording: a price list is *not* the only way to know what a call cost, because a provider
  may report the cost itself, and because a table carrying one input price and one output price is
  wrong for the provider this repository ships by default — DeepSeek prices two input lanes (cache hit
  and cache miss) at a peak rate and an off-peak rate, and one output lane the same way. The evidence
  below is the `7e6b017` baseline, so its `rg` exit code describes
  that tree rather than the current one.
- **Gap**: nothing bounds a run or a session by tokens or money, and nothing stops a run that is
  burning either.
- **Evidence**: `rg -in 'circuit' . -g '!*_test.go'` → exit 1; no cost, spend, price or quota
  accounting anywhere.
- **Already present**: a per-run *step* budget — `MaxSteps` 20 / 50 / 15 / 20 per agent
  (`agent/config.go:64,104,123,140`), enforced at `agent/agent.go:190` — and a per-request output
  cap `MaxTokens = 4096` (`agent/agent.go:105` → `"max_tokens"`, `agent/provider_openai.go:103`).
  Neither is a cumulative budget. **Needed by D7**: a session token budget can end a goal before its
  round cap, so `/goal` has to show both numbers side by side.
- **Effort**: ~1 d.

### A4 — No prompt caching

- **Gap**: the system prompt and the JSON Schema of all 24 tools are resent every step, on
  providers where a stable prefix could be cached.
- **Evidence**: `rg -c 'cache_control'`, `'prompt_cache'`, `'cached_tokens'` → exit 1 each, over
  the whole repository including the nested module.
- **Effort**: ~2–3 d.

### A5 — No failover and no provider routing

- **Gap**: a provider cannot be failed over automatically, and the provider half of a
  `"<provider>/<model>"` setting is ignored.
- **Evidence**: `SwitchTo`/`SwitchToName` are called only from the `/model` picker
  (`tui/update.go:175`, restored at `tui/model.go:289`); `rg -in 'failover'` → exit 1;
  `main.go:793` takes only the part after the slash (`aCfg.Model = after // use model part, ignore
  provider for now`).
- **Already present**: the per-agent model override **is** read and applied —
  `AgentOverride.Model` (`config/config.go:43-49`) → `main.go:790-796` → `agent/agent.go:116-119` →
  `ChatRequest.Model` — and `task` sub-agents inherit it (`tool/task.go:97`). The source report's
  claim that this key has no routing logic behind it is wrong; only provider routing is missing.
- **Effort**: ~2 d.

### A6 — Only two provider backends

- **Gap**: no native Anthropic, Gemini, Bedrock or Azure backend. Going through the compatible
  layer loses each vendor's own reasoning fields and its own billing semantics.
- **Evidence**: `rg -il 'gemini'|'bedrock'|'azure'|'vertex'` → exit 1 each; `main.go:727` selects
  `case "ollama"`, everything else is OpenAI-compatible.
- **Already present**: an OpenAI-compatible backend (`agent/provider_openai.go`, raw `net/http`) and
  a native Ollama backend (`agent/provider_ollama.go:192`, `POST /api/chat`). Note that
  `config.example.json:25` configures `"anthropic/claude-sonnet-4"` under `"type": "openai"`, i.e.
  the shipped example routes a Claude model through OpenRouter — not through a native backend.
- **Effort**: ~3–5 d per vendor.

---

## B. Context and memory

### B1 — The memory capability exists but is never called

- **Gap**: no cross-session memory in practice. The client is dead code: not registered as a tool,
  not instantiated on any path.
- **Decision (issue #188)**: `memory` is not a tool and is not meant to become one. Recall is an
  agent-layer step — `Agent.Memory` + `MemoryModeAuto`, at the top of `Run`, never a model-callable
  function — so no diagram, table or whitelist may list it among the tools. What is missing here is a
  caller, not a tool definition, and this entry is the record of that gap.
- **Evidence**: `HTTPMemoryClient` is referenced only by its own file, its own test and `CODEBASE.md`;
  `Agent.Memory` (declared `agent/agent.go:21`, read at `agent/agent.go:264-265`) is never assigned
  outside that declaration, and `NewHTTPMemoryClient` has no non-test caller; no `memory` key exists in
  `config.example.json` or `config/`. The last artefact that named a `memory` tool — the TUI
  housekeeping whitelist at `tui/update.go:578` — was removed by #188, so no tool list names it now.
- **Already present**: `agent/memory.go` implements `Remember`, `Recall`, `Forget` and `List` against
  a remote HTTP service. Wiring is cheap; the design question below is not.
- **Effort**: ~1–2 d to wire.

### B2 — No local memory backend

- **Gap**: the only backend is a remote HTTP service, so offline, self-hosted and
  privacy-sensitive use is impossible; there is also no way to see, review or approve what is
  remembered.
- **Evidence**: `MemoryStore`'s only implementation is `HTTPMemoryClient` (`agent/memory.go:74-120`),
  defaulting to `https://memory.yusiwen.cn/dp-api`, bank `hermes` (`agent/memory.go:15-16`);
  `Forget` is a no-op that returns nil (`agent/memory.go:102-105`), so deletion is not merely
  unexposed but unimplemented; `rg -il 'sqlite|local.?memory|memory.?backend'` → exit 1.
- **Effort**: ~3–5 d.

### B3 — No embeddings, vector search or rerank

- **Gap**: context can only be a whole file or a keyword hit; there is no semantic retrieval.
- **Evidence**: `rg -il 'embedding'|'cosine'|'rerank'|'faiss'|'hnsw'|'knn'|'nearest neighbor'|'dot
  product'` → exit 1 each.
- **Already present**: Levenshtein string similarity, used to anchor fuzzy edits
  (`tool/edit.go:332,367,698-700`) — unrelated to vector similarity.
- **Effort**: ~5–8 d.

### B4 — No repository index or repo map

- **Gap**: no tree-sitter, no symbol index, no call graph, no repo map. The agent cannot answer
  "who calls this symbol" or "what does changing this affect".
- **Evidence**: searches for `tree-sitter`, `symbol.?index`, `call.?graph`, `codebase.?index`,
  `repo.?map`, `go/parser`, `go/ast` → exit 1 each; `search_files` ladders `rg` → `grep` → Go
  `filepath.Walk` (`tool/filesystem.go:196,243-262,370`), i.e. text search; the four LSP tools are
  per-file point queries (`lsp/tools.go:17-20` — definition, references, hover, document symbols).
- **Already present**: gopls maintains its own index, but this repository builds none and exposes no
  repo-map or call-graph surface.
- **Effort**: ~8–15 d. The largest single bet in this document — see the note on spikes below.

### B5 — Context management has a single strategy

- **Gap**: compression is automatic and all-or-nothing. No `/drop`, no `/pin`, no unloading by
  message or by file, and no preview of what a compression would discard.
- **Evidence**: one strategy, `compressHistory` (`agent/compression.go:59`) — head, LLM-summarized
  middle, tail — triggered at `CompressionThreshold`, default 500000 of a 1000000 window
  (`config/config.go:164-165`), falling back to `ContextLength/2` (`main.go:140-141`) and
  re-derived as `limit/2` after a context-length downgrade (`agent/compression.go:238`);
  `rg '"/drop"|"/pin"|unload'` → exit 1.
- **Already present**: `/compress` triggers the same strategy by hand (`tui/update.go:1050`).
  **Needed by D7**: continuation round after round depends on compaction running *inside* a run, not
  only before one starts — that is the part this entry records as missing.
- **Effort**: ~3–5 d.

### B6 — Truncation is fixed, not budget-aware

- **Gap**: truncation is a line/byte cap, not a token budget, and there is no structured summary in
  place of raw text.
- **Evidence**: `config.example.json:53-57` sets `max_lines` 2000, `max_bytes` 204800,
  `output_dir` `/tmp/tinycode/truncated`, matching the code defaults (`agent/truncate.go:15-25`);
  overflow is written 0600 to a unique file and the tool result carries a head-only preview plus a
  `[TRUNCATED] Full output saved to: … Use read_file with offset/limit` pointer
  (`agent/truncate.go:53-99`).
- **Already present**: the limits are configurable through `SetTruncationConfig`
  (`agent/truncate.go:29-40`), which ignores zero or negative values — so truncation can be moved
  but not disabled.
- **Effort**: ~1–2 d.

---

## C. Engineering loop

### C1 — No checkpoint and no undo

- **Gap**: edits land straight on disk. There is no per-step snapshot and no way to return to the
  state before the last tool call — the single hardest gap in this document, because it is what a
  user must trust before letting the agent write.
- **Picked up**: [#234](https://github.com/yusiwen/TinyCode/issues/234) — S1 (the store, the
  pre-images and a restore), S2 (`/undo` and `--undo`), S3 (documents, retention and the boundary).
  **Delivered.** Every file tool's write records the bytes it is about to replace, through the one
  choke point all four write sites and the delete operation pass through, so no path can forget; a
  context without a tool-call scope records nothing, and a pre-image that cannot be read refuses the
  write rather than performing it unundoably. `/undo` lists the files and confirms before restoring,
  `--undo` does the same without a TUI, and marking the call undone in its own manifest is what makes
  the next undo reach the call before it. The store sits outside the project root (`checkpoint_dir`,
  default `~/.tinycode/checkpoints`), is not a tool path — a write to it is refused with
  `ask: reserved`, and only `sandbox_full_access` bypasses that — and is bounded to 50 calls or
  64 MiB per project, dropping the oldest **already-undone** calls first and then collecting objects
  nothing references. A call that can still be undone is never dropped, so the bound loses to
  undo-ability on purpose. The boundary is stated where a user reads it,
  [checkpoints.md](checkpoints.md). A `bash` command's writes were the boundary this entry named as
  unfinished; [#238](https://github.com/yusiwen/TinyCode/issues/238) closed it by snapshotting the
  project tree before a command and diffing after it, bounded per file and per snapshot, with the
  skip reported in the tool result. What remains is named there: paths outside the project root, and
  effects outside the filesystem.
- **Evidence**: `rg -qi 'checkpoint' .` → exit 1; the 17 TUI slash commands (`tui/model.go:193-205`)
  contain no `/undo`; `edit` and `apply_patch` write through `writeSandboxed` (`tool/edit.go:156`,
  `tool/apply_patch.go:180` → `tool/sandboxio.go:52-93`), which renames a temp file over the target
  and removes the temp — the previous bytes are never retained, and the atomic replace is a
  property of the write, not a recovery mechanism. The evidence below is the `7e6b017` baseline, so
  its `rg` exit code describes that tree rather than the current one.
- **Already present**: `SnapshotBaseline` (`lsp/touch.go:176-179`) snapshots *diagnostics* for
  delta reporting, not file content. Design note: `.tinycode/` is already in `.gitignore`, so a
  shadow repository under it would be invisible to the user's own git.
- **Effort**: ~5–8 d.

### C2 — No automatic change preview and commit loop, and no remote operations

- **Gap**: no run-end diff summary with a prompt to commit, and the git toolset cannot push or open
  a pull request.
- **Evidence**: exactly five git tools, all in `tool/git.go` — `git_status`, `git_diff`,
  `git_commit`, `git_branch`, `git_log`; `rg -F 'push' tool/git.go` → exit 1; no `gh` invocation.
- **Already present**: `git_diff` previews and `git_commit` runs `git add -A` + `git commit`
  (`tool/git.go:38,65-83`), so a *manual* preview-then-commit loop already exists. What is missing is
  the automatic loop and the remote half.
- **Effort**: ~3–5 d.

### C3 — No isolation for parallel execution

- **Gap**: sub-agents share one working directory, so two parallel `task` calls can write the same
  file. No worktree isolation, no file locks, no conflict detection or merge.
- **Evidence**: sub-agents are in-process goroutines (`tool/task.go`, `tool/bgtask.go`) sharing the
  process working directory; no `flock`, `FileLock` or `TryLock`; no `os.Chdir` outside tests; the
  only directory control is the per-call `workdir` argument (`tool/bash.go:306-307`).
- **Note**: the empty `.worktrees/` directory at the repository root is an artefact of the agent
  workflow, not a product location waiting to be implemented — it is git-ignored (issue #167).
- **Effort**: ~5–8 d.

### C4 — No user-configurable hooks and no session lifecycle events

- **Gap**: a user cannot declare "run gofmt after a `.go` write", "run the gate before a commit" or
  "export the trace at session end".
- **Evidence**: no `PreToolUse`/`PostToolUse`; no `hook` key in `config/config.go` or
  `config.example.json`; `OnSession`, `OnStart`, `OnExit` → exit 1.
- **Already present**: in-process `StreamCallbacks` (`types/types.go:45-52`) already provide
  `OnToolCall` before each tool (`agent/agent.go:328`), `OnToolResult` after it
  (`agent/agent.go:388`) and `OnStepDone` per step (`agent/agent.go:444`), wired by the TUI
  (`tui/update.go:1377-1398`). They drive display only and are not user-extensible — that is the gap.
  **Needed by D7**: the continuation driver needs a turn-end seam; the TUI's `StreamDone` is this
  bundle's only one, and a general lifecycle event remains this entry's gap.
- **Effort**: ~2–3 d.

### C5 — No test feedback loop

- **Gap**: after a change, nothing discovers and runs the project's tests, and a test failure is
  never fed back to the model.
- **Evidence**: no product-source reference to `go test`, `runTests` or `testCommand`; only
  `Makefile` development targets.
- **Already present**: compile-level diagnostics *are* fed back — `edit`, `apply_patch` and
  `write_file` capture a baseline before the write and append the delta to the tool result
  (`tool/edit.go:163-167`; baselines at `tool/edit.go:105`, `tool/apply_patch.go:174`,
  `tool/filesystem.go:165`), ERROR severity only and capped at 20 per file
  (`lsp/formatter.go:8,17,28-31`). The gap is tests, not diagnostics.
- **Effort**: ~3–5 d.

### C6 — Isolation has only one tier

- **Gap**: no container, VM or remote execution backend, and no way to escalate to a deliberately
  wider or narrower confinement tier.
- **Evidence**: no `docker`, `podman`, `ssh`, `qemu`, `firecracker` or `nsenter`; `SandboxFullAccess`
  appears only as a declaration and in comparisons (`types/types.go:87`, `tool/sandbox.go:285`,
  `tool/sandboxexec.go:181`) with no assignment anywhere, and the mode is chosen unconditionally
  (`agent/agent.go:177-179`).
- **Already present**: the mode is *honoured* when supplied — `tool/sandboxexec.go:181-184` runs an
  unconfined bash — so it is a reachable library value, not dead vocabulary. Linux command
  confinement is real: path fence plus Landlock (`tool/confinement_linux.go`).
- **Effort**: ~10–15 d.

### C7 — No command confinement on macOS

- **Picked up**: [#247](https://github.com/yusiwen/TinyCode/issues/247) — S1 (the Seatbelt profile, the
  launcher through `sandbox-exec`, and a capability answer that is a smoke run), S2 (the real-kernel
  acceptance test and the documents that said macOS had no mechanism). **Delivered 2026-10-18**: the
  launcher renders a profile from the same mode and roots a Linux launcher turns into a Landlock
  ruleset — `(deny file-write*)`, `(literal "/dev/null")`, then one `(subpath …)` per **resolved** root,
  because Seatbelt matches the path the kernel resolved and `/tmp`, `/var`, `/etc` are links into
  `/private` — and re-execs through `/usr/bin/sandbox-exec`, which execvp's the command so its
  `argv[0]` survives as it does on Linux. Because the default was already derived from the capability
  rather than from the platform name, the config change this needed was none: the mechanism landing is
  what flips `confine_commands`. `make test-tui-confine`, which skipped here with a printed reason, now
  passes on macOS, asserting on the filesystem after the real binary exits. It is accepted with its
  deprecation stated: `sandbox-exec` is Apple's and deprecated, and every failure path refuses the
  command rather than running it unconfined, so the worst case is a host that refuses commands instead
  of one that silently frees them.
- **Gap** (the state before #247): the component walk confines the agent's own opens, but nothing
  confined a command the agent spawns. Command confinement was off by default on macOS, and turning it
  on yielded a capability refusal.
- **Evidence**: no `sandbox_init`, no Seatbelt or libsandbox binding; `tool/confinement_other.go` is
  `//go:build !linux` and returns `launcherFail("no kernel file-boundary mechanism for subprocesses
  on this platform")` with `commandConfinementAvailable() == false`. The four `sandbox-exec` matches
  are this repository's own launcher constant, `SandboxLauncherCommand = "__sandbox-exec"`
  (`tool/sandboxexec.go:21`) — Apple's `/usr/bin/sandbox-exec` was never invoked.
  **Updated 2026-10-18**: it is, by #247, which is what `confinement_darwin.go` is; the counts above
  are the `7e6b017` baseline this entry was written against.
- **Already present**: the macOS component walk (`tool/pathbeneath_walk.go`), reported as
  `ContainmentKernel` (`tool/containment_darwin.go`). Documented as a limit at `docs/sandbox.md:375`.
- **Effort**: ~5–8 d.

### C8 — No Windows support

- **Gap**: no Windows build target and no platform-specific files.
- **Evidence**: `Makefile:16-19` lists linux-amd64, linux-arm64 and darwin-arm64; no `*_windows.go`
  (control: three `*_darwin.go` files exist); the CI cross matrix matches
  (`.github/workflows/main.yml:146-148`).
- **Effort**: ~10–20 d.

---

## D. Interaction and multimodality

### D1 — No image input

- **Gap**: a screenshot, a design or a picture of an error cannot be given to the agent.
- **Evidence**: `rg -qi 'image_url|multimodal|ContentPart|ImageContent'` → exit 1; `types.Message`
  carries `Content string` only (`types/types.go:14-21`) and `ChatRequest` has no image field
  (`types/types.go:38-45`).
- **Effort**: ~2–3 d (source report estimate; likely optimistic, since it covers both the provider
  capability and paste support in the TUI).

### D2 — No audio

- **Gap**: no speech input and no speech output.
- **Evidence**: `audio`, `speech`, `tts`, `whisper` — every count is 0.
- **Effort**: ~5–10 d.

### D3 — No desktop computer use

- **Gap**: browser automation is the only automation path; there is no screen capture and no desktop
  click/type control, and no mobile target.
- **Evidence**: `go-rod` is present (`go.mod:9`) but `tool/web_browser.go` uses only `Connect`,
  `Page`, `HijackRequests`, `Eval`, `WaitLoad` and `Close` — no click, type, key or mouse call; no
  screen capture, `xdotool`, `cliclick` or `CGEvent`; no Android or iOS surface.
- **Effort**: ~10–20 d.

### D4 — No event-driven entry point

- **Gap**: a run must be started by a human at the TUI. No webhook, no cron, no file-watch trigger.
- **Evidence**: `webhook`, `cron`, `fsnotify`, `watcher`, `inotify` → all 0. Background work is
  in-process: `tool/bgtask.go` holds a `map[string]*BgTask`, starts a `go func()` with a 120 s
  timeout and has no persistence, socket or daemon — the state dies with the process.
- **Not D7**: this entry is about an *external* trigger starting a run; D7 is an *internal* idle driver
  continuing one, and it needs durable goal state that this entry does not.
- **Effort**: ~5–8 d.

### D5 — No notification when a long task finishes

- **Picked up**: [#198](https://github.com/yusiwen/TinyCode/issues/198) — S1 (`internal/notify`: the
  terminal escapes, the platform notifier, the threshold gate, and the two seams a test asserts
  through), S2 (the `notify` config section), S3 (the triggers: a background task's completion, and a
  foreground run that outlived the threshold), S4 (tests and docs). The evidence below is the
  `31a34e0` baseline and its counts still hold; what it does not say, and what the pick-up had to
  decide, is the policy — channel, threshold, and whether the feature is on by default.
- **Gap**: a finished background run does not announce itself; the user has to watch the interface.
- **Evidence**: `osascript`, `notify-send`, `terminal-notifier` → all 0.
- **Effort**: ~0.5 d. The cheapest item in this document.

### D6 — No virtual terminal pane

- **Gap**: the agent cannot start a real interactive shell and drive it, so a REPL, a TTY-requiring
  tool or an interactive rebase cannot be run under supervision.
- **Evidence**: no product-code PTY — in the main module the only importer of `creack/pty` is
  `tui/verification_test.go:31`, and `tuiprobe/` is a nested test-harness module rather than a
  product feature. `bash` is one-shot with no stdin (`tool/bash.go:251`), a default 30 s timeout and
  a process-tree and process-group kill (`tool/sysproc_unix.go:20,36-44`).
- **Effort**: ~8–15 d.

### D7 — No persistent goal and no continuation driver

- **Picked up**: [#259](https://github.com/yusiwen/TinyCode/issues/259) — S1 (the goal domain and its
  persistence in the session file), S2 (`/goal`: view, create, edit, pause, resume, clear), S3 (the
  model-facing `goal` tool and its authority rules), S4 (the round prompt and the idle driver), S5
  (the status-bar segment, the completion announcement and the trajectory attribution), S6 (the
  `goal` config section and the documents). **Prerequisite**:
  [#258](https://github.com/yusiwen/TinyCode/issues/258) — a continuation cannot read a context the
  process never restored, so the resume/history defect lands first. The cap defaults to **256
  rounds** (configurable smaller), with a `no-progress` guard at three consecutive tool-free rounds;
  mid-run steering stays out of scope, and the input-empty guard plus a grace period is the interim
  answer.
- **Delivered 2026-10-19** (six PRs, merged as #262–#267 on top of #261): one objective per session
  persists in the session file with its phase, round count and blocker; `/goal` and a `goal` tool
  steer it under an authority rule — a round may complete or block the goal, never create, edit,
  pause or resume it; an idle driver starts a round after a grace period and counts it *before* the
  work, so a crash loop cannot spend the budget twice; the status bar, the completion channel and the
  run record all report it. The defaults are 256 rounds, a two-second grace and a three-round
  `no-progress` guard, configurable in the `goal` section, and [goals.md](goals.md) is the user's
  page. `goal-resume.scenario` is the cross-process gate: a resumed session shows the goal disarmed
  and proves it by running a command instead of starting a round. What stays outside this entry: no
  mid-run steering (a round in flight refuses input, so the input-empty guard is the interim
  answer), no independent evaluator of "complete", and a PTY scenario for a *running* round
  (deferred once S4's `Update` tests showed they cover the driver).
- **Gap**: a session holds a conversation, not an objective. Only the transcript survives "stop and
  come back tomorrow", nothing continues a run when the agent goes idle, so a multi-hour task is a
  human typing "continue"; there is no round budget and no durable done/blocked state.
- **Evidence**: `session.Session` has no goal, phase or round field (`session/session.go:18-45`); the
  17 slash commands contain no `/goal` (`tui/update.go:1115-1459`); a second submission while a run
  is streaming is refused (`tui/update.go:298-310`); `rg -n 'goal' agent/ tui/ session/` → only the
  `task` tool's per-subagent string (`agent/config.go:96-98`); the only bounds on a run are
  `MaxSteps` (`agent/config.go:64-140`) and the token budget (`agent/agent.go:220-228`).
- **Already present**: one precedent for carrying state across a boundary — the todo store is
  re-injected after compression (`agent/compression.go:177`) and restored by reverse-scanning the
  transcript on resume (`tui/model.go:321-333`); `internal/notify` already announces a finished long
  task (`tool/bgtask.go:178`); one trajectory is already written per run with a stop reason
  (`agent/trajectory_agent.go:19-24`), so a round is a run with an attribution rather than a new
  record type.
- **Not D4**: D4 is an *external* trigger starting a run (webhook, cron, file watch). This is an
  *internal* idle driver continuing one, and it needs durable goal state that D4 does not.
- **Not in the source inventory**: unlike the other entries, this gap was not in the 2026-10-09
  report. It was recorded 2026-10-19 from a capability-by-capability comparison with a harness that
  has persistent goals, and every reference here was checked the same way as the rest — a counted
  search or an exit code, never a sample.
- **Effort**: ~6–8 d (estimate).

---

## E. Service surface and observability

### E1 — Cannot serve as an MCP server

- **Gap**: TinyCode is an MCP client only. It cannot be driven by another MCP host, and it does not
  implement the server side of sampling or elicitation.
- **Evidence**: the client lives in `mcp/mcp.go` with a stdio and an HTTP transport; server-initiated
  requests are answered for `ping` and `roots/list` and otherwise with `-32601`
  (`mcp/mcp.go:336`, asserted for `sampling/createMessage` in `mcp/server_request_test.go:34-35`).
- **Effort**: ~3–5 d.

### E2 — No service mode

- **Gap**: no HTTP or SSE API, so an IDE, a web front-end or another orchestrator cannot drive it.
- **Evidence**: the entry points are the TUI, a one-shot prompt (`main.go`) and the internal
  `__sandbox-exec` launcher subcommand; there is no `serve` command and no `http.ListenAndServe` in
  `main.go`.
- **Effort**: ~3–5 d.

### E3 — No IDE plugin

- **Gap**: no VS Code or JetBrains surface, no inline diff. LSP is consumed as a tool, never offered.
- **Evidence**: no IDE integration in the repository.
- **Effort**: ~10–20 d.

### E4 — Trajectories are not persisted in an analysable format

- **Picked up**: [#249](https://github.com/yusiwen/TinyCode/issues/249) — S1 (the writer, the schema and
  the loop's emit sites), S2 (configuration, wiring, the delegated-agent hand-off, retention and the
  reserved path), S3 (`--trajectories`, `--trajectory`, and the documents). **Delivered 2026-10-18**:
  a run writes one JSONL file, one event per line, each event carrying its own duration — `run.start`,
  `llm.call` (duration, TTFT, tokens, cost and the call's outcome), `tool.call` (duration, argument and
  result digests, and a status), `step.end`, `run.end` (which of `final`/`budget`/`blocked`/
  `step_limit`/`cancelled`/`error` stopped it). Recording is on by default and keeps **metadata only**
  unless `capture: "full"` comes from the user's own config file; the store is bounded at 200 runs /
  64 MiB, pruned once per run, and refuses tool writes with `ask: reserved` because a record the
  recorded agent can rewrite is not evidence. A recording failure warns once and never fails a run —
  the opposite of the checkpoint rule, on purpose. A delegated agent writes its own file, numbered and
  linked by the parent, which is the data G1's attribution needs. `docs/trajectories.md` is the
  user-facing page.
- **Gap** (the state before #249): no trace or span structure, no per-step timing plus token record, no
  machine-readable export of a run. A trajectory is the raw material for evaluation, and only a
  human-readable session file existed.
- **Evidence**: structured logging (`tlog`) and session JSON are present; `tlog`'s `"trace"` is a log
  *level* (`tlog/tlog.go:38`), not tracing; nothing writes per-step spans. (Re-checked 2026-10-18: still
  why a log line is not a record, which is what #249's `agent/trajectory*.go` answers.)
- **Effort**: ~2–3 d, and it was the prerequisite for F1.

### E5 — Refuted: project instruction files *are* loaded

The source report claims TinyCode does not read `AGENTS.md` or `CLAUDE.md`. It does:
`loadProjectContext()` (`main.go:667-679`) reads the first of `AGENTS.md`, `CLAUDE.md`,
`.tinycode.md` found in the working directory, and `main.go:151` injects it into the system prompt
inside a `<project-context>` block. Four tests cover it, including precedence when both files exist
(`main_test.go:22-70`). **No work item.** The residual, much narrower gap is that it reads the
working directory only — it never walks upward and never merges nested files (see the deltas below).

---

## F. Evaluation and quality signals

### F1 — No agent-level evaluation framework

- **Picked up**: [#252](https://github.com/yusiwen/TinyCode/issues/252) — the spike this entry asks for, and
  deliberately not the framework. **Measured 2026-10-18**: six tasks, three configurations (the default build
  agent, the same with a hard token budget, and the read-only plan agent), two repetitions each, graded by
  each task's own `go test ./...` and read from the runs' trajectories. The signal exists — build 6/6, limited
  5/6, plan 0/6, with the two ends agreeing with themselves on every task — but **the task set is saturated**:
  five of the six tasks are solved by step 3, so the budget only binds on the hardest one, which is also the
  only cell whose repetitions disagreed. The harness, the verdict rule and the baseline are in
  [eval.md](eval.md); the committed numbers are `eval/BASELINE.md`. **The next increment is a task set with a
  difficulty spread, not a framework** — a framework built on this set would measure the ceiling rather than
  the agent.
- **Difficulty gradient** ([#254](https://github.com/yusiwen/TinyCode/issues/254)) — six more tasks in two
  declared tiers (multi-file contracts; symptom-only prompts whose tests are the specification), a `TIER` per
  task, per-tier reporting, and a reference fix for every task kept outside the task directory and proved by a
  test that each task is red before it and green after. **Measured 2026-10-18, 72 runs: the declared gradient
  does not exist.** build solves 6/6 of tier 1 and 3/3 of tier 3, and the spread *inside* tier 1 (23k to
  134k tokens for one task) is wider than any gap between tiers. What the numbers do separate is an invariant
  that has to survive a refusal — the two tasks the halved budget failed, and the two most expensive ones, are
  the ones where a rejected operation must leave state untouched. File count, prompt ambiguity and "read the
  tests first" were not obstacles. **The next increment builds tasks around failure-path state, and measures
  the tiers from the first run instead of declaring them.**
- **Headroom** ([#256](https://github.com/yusiwen/TinyCode/issues/256)) — six tasks chosen to be hard for a
  stated reason (an algorithmic bound the suite times, an implementation from a specification, five defects at
  once, one wire format produced by three sites, all-or-nothing state, an exhaustive format with a round-trip),
  and difficulty **measured** instead of declared: `TIER` is now an expectation and the report carries a band
  per task from the default configuration's own token spend. **Measured 2026-10-18, 108 runs: no task defeated
  the default configuration.** `build` solved 18/18, two repetitions each. The knob moves *cost* — tokens per
  solved task went from 88 091 to 184 183, and the spread across tasks from 18 440 to 465 719 — and it defeats
  a budget-starved agent (`limited` fell from 9/12 to 6/18), but it does not defeat a funded one. The task I
  expected to be hardest, `merge-inversions` with an O(n log n) bound, came out in the **cheapest** band (6
  steps); what cost the most was implementing `expr-eval` from a specification (28 steps, 465 719 tokens).
  **The conclusion was written before the data: the next change is the domain, not the difficulty knob** — a
  real repository, a multi-hour refactor, a failure mode that is not "one package, one test suite".
  **D7 supplies the execution mechanism for that domain**: a task set can span more work than one
  request, without a human typing "continue" between rounds.
- **Prepared for**: the record it needs exists now — one JSONL file per run with steps, durations,
  tokens, costs and per-tool outcomes (E4, issue #249), listed with `tinycode --trajectories` and
  documented in [trajectories.md](trajectories.md). What F1 still owns is the task set, the
  repetitions, pass@k and a judge: a record of what happened is not a verdict on whether it was good.
- **Gap**: there is no task set, no pass@k, no LLM-as-judge and no regression baseline, so "did the
  agent get better?" has no signal when a prompt, a model or a compression threshold changes.
- **Evidence**: the suite is software testing — 749 test functions in the main module (848 counting
  the nested module's 99) and 10 fuzz targets, with `MockLLM` integration tests and golden frames —
  and none of it measures agent quality on a task.
- **Effort**: ~8–15 d.

### F2 — No A/B comparison mechanism

- **Gap**: two system prompts, two skill sets or two models cannot be run over one task set and
  compared.
- **Evidence**: no such mechanism; and without F1's task set or A2's per-run accounting there is
  nothing to compare with.
- **Effort**: ~2 d (after F1).

### F3 — Tool calls are not validated against their schema

- **Picked up**: [#200](https://github.com/yusiwen/TinyCode/issues/200) — S1 (`ValidateArgs` in
  `agent/toolcall.go`, implementing the five keywords the built-in schemas actually use), S2 (wired in
  before `Execute`, so a call that breaks its schema is not executed and the model is told every problem
  at once), S3 (the repeat circuit breaker: per-run, keyed on call identity, counting failures only),
  S4 (tests and docs). The evidence below is the `9ff62fc` baseline and still holds; picking it up added
  the keyword census that scopes S1, which is the part the entry did not have.
- **Gap**: arguments are parsed but never checked against the tool's JSON Schema, so a missing
  required field, a wrong type or a bad enum reaches `Execute` and is reported as a tool error for
  the model to guess its way out of. There is also no dedup or circuit breaker for a model that
  repeats the same call.
- **Evidence**: the dispatcher unmarshals the argument string and hands a parse failure back as text
  (`agent/agent.go:370-371`), and an unknown name back as `unknown tool: …`
  (`agent/agent.go:383`). Nothing validates required fields, types or enums, and nothing counts
  repeated identical calls.
- **Effort**: ~2–3 d. Cheap, and it removes wasted steps directly.

---

## G. Multi-agent

### G1 — Delegation is two levels deep

- **Gap**: only `task` and `task_collect`, with two sub-agents (`explore`, `general`,
  `agent/config.go:109,130`). No role separation (planner, coder, reviewer, verifier), no
  communication between sub-agents, no review or arbitration of a result, no explicit policy for
  shared versus isolated context, and no cost attribution — which is not merely missing but
  unattributable today, because there is no per-run accounting (A2).
- **Evidence**: `tool/task.go` dispatches by agent name into a fresh in-process agent; nothing
  coordinates two of them.
- **Effort**: ~5–10 d together with G2.

### G2 — Sub-agent output is not streamed to the UI

- **Gap**: a long sub-agent run shows only "running"; the user cannot see what it is doing.
- **Evidence**: there is no sub-agent progress callback, and `StreamCallbacks` has no such member
  (`types/types.go:45-52`).

---

## H. Other

### H1 — No i18n

- **Gap**: UI strings are Chinese, with no language switch.

### H2 — Command checks are heuristics, not a boundary

- **Gap**: the command blacklist and the plan-mode write interception are substring matches. On a
  platform with no kernel mechanism they are the entire posture.
- **Evidence**: `CODEBASE.md:543` — "`CheckCommand` and plan-mode checks are advisory substring
  heuristics, not an OS-level boundary"; `docs/sandbox.md:368-374` — "Confinement is on by default
  only where it can be enforced… the string checks are the posture there."

### H3 — No branch visualisation

- **Gap**: `session.Store.Fork` can branch a session but the TUI reports one line, so the
  relationship between two branches has to be inferred from IDs.
- **Evidence**: `session/session.go:222` implements `Fork`; the TUI calls it at
  `tui/update.go:1146` and prints `Created branch: …`; there is no tree view and no branch
  comparison.

---

## Verification deltas

Where the source report described a mechanism as absent while a narrower one exists, the entry
above records the real gap. The differences, so the overstated version is not re-derived:

| Code | The source report's version | What the re-check found |
| :--- | :--- | :--- |
| A2 | "the TUI has no token accounting implementation" | It has an estimated counter driving the status bar (`tui/view.go:428`, `tui/update.go:482-496`); what is missing is provider-reported usage |
| A3 | "no maximum token anywhere" | A per-run step budget and a per-request `max_tokens` cap exist; the cumulative budget and circuit breaker do not |
| A5 | "`agents.<name>.model` exists but no routing logic" | The key is read and applied per agent, including sub-agents; only provider routing and failover are missing |
| C2 | "no change preview with a commit loop" | `git_diff` + `git_commit` already form a manual preview-then-commit loop; the automatic loop and push/PR are missing |
| C3 | "worktree isolation has a location but no implementation" | `.worktrees/` is an agent-workflow artefact, not a product location (issue #167) |
| C4 | "no hooks or lifecycle events" | In-process tool-before, tool-after and per-step callbacks exist; configurable hooks and session lifecycle events do not |
| C5 | "no feeding of failures back to the model" | Compile-level LSP deltas *are* appended to write results; only test failures are not fed back |
| E4 | — | Correct, but `tlog`'s `"trace"` is a log level, not tracing |

Two counting errors in the source report are also worth recording:

- It says the inventory has 38 items; it has **39**.
- Its P0 table's rows sum to **31.5–51.5** man-days, while the table is introduced as "~30–35". The
  low end is about right; the high end is understated by roughly 16 days.
- Its test-count figures disagree with each other and with the repository: the README badge says
  710, a README changelog entry says 359, `CODEBASE.md` prose says 771, and the comment beside
  `CODEBASE.md:582`'s own command says 710. The measured values are **749** test functions in the
  main module and **848** repository-wide. Tracked separately, because it is a defect in this
  repository rather than in the source report: issue #170.

## A recommended reading order, not a commitment

The source report's P0 mixes three 8–15 day bets with four half-day-to-two-day certainties. Sorting
by certainty of return rather than by size:

1. **Certainties, days not weeks** — A1 (retry and backoff), A2 (usage), D5 (notification),
   F3 (tool-call validation). Each is independently shippable and removes a random failure or a
   wasted step. **Delivered** (their entries carry the **Picked up** lines), together with A3's token
   and cost halves, so this tier is empty: start at item 2 or 3 rather than here.
2. **Existing code waiting for a wire** — B1 and B2 (memory), which need a privacy decision before
   an implementation: pointing a default at a remote service is a design choice, not a plumbing
   task.
3. **The trust gap** — C1 (checkpoint and undo), the one item whose absence is felt on the first
   real mistake. **Delivered** (issue #234), and its last boundary — a `bash` command's writes —
   closed by [#238](https://github.com/yusiwen/TinyCode/issues/238); the limits that remain (paths
   outside the project root, effects outside the filesystem) are in
   [checkpoints.md](checkpoints.md).
4. **Escalate C7 above C6.** The Linux fence is real (path fence, Landlock, cgroup v2 kill); a
   container backend is a different deployment story rather than more of the same. Meanwhile macOS
   command confinement is *off on this project's own development machine*, which is a cost paid
   daily. **Delivered** (issue #247): the macOS launcher applies Seatbelt through
   `/usr/bin/sandbox-exec` and the default flipped with the capability, so this tier's cost is paid
   and C6 is what remains open.
5. **Bets that need a spike before an implementation** — B4 (repo index), F1 (evaluation), and the
   larger D and E items. For these the first increment should answer "is this worth it" with
   measurements on real tasks, and the issue should carry the condition under which the work is
   abandoned.
6. **Long-horizon execution** — D7 (persistent goal and continuation driver). **Delivered** (issue
   #259, prerequisite [#258](https://github.com/yusiwen/TinyCode/issues/258) landed first). It is
   the mechanism F1's next task set needs: the domain can now be a real repository and a multi-hour
   refactor, because a round is a whole turn and the goal continues it. What it leaves open is
   mid-run steering and an independent judgement of "complete".
