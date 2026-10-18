# Model pricing: what this repository ships, and what it will not

**Decided 2026-10-14**, on issue [#182](https://github.com/yusiwen/TinyCode/issues/182), after
[#181](https://github.com/yusiwen/TinyCode/issues/181) delivered the mechanism this decision is about.
Inputs are dated where they were read, because a pricing question ages faster than the code around it.

**Updated 2026-10-18**: [#225](https://github.com/yusiwen/TinyCode/issues/225) landed, so a declared rate
can now carry a time window (`pricing.prices.<key>.windows`, with the weekdays and UTC hours it covers).
The verdict itself is unchanged — nothing is vendored — but the blocker this document names in sections 6
and 7 is gone, which makes the embed option a cost question rather than a blocked one. The record below is
kept as it was written on 2026-10-14; the statements that are no longer true carry a dated mark where they
stand.

## The verdict

**This repository does not ship a model price table, and whether it ever should is left open on a named
condition rather than answered.** No dataset is embedded, nothing is fetched at runtime, and there is no
regeneration script to run; what ships is the two mechanisms below. The condition is the schema: a table
cannot be right for our own default route until `Price` can hold what it would say, which is
[#225](https://github.com/yusiwen/TinyCode/issues/225) (delivered 2026-10-18). So this is a deferred
question with its blocker named — not an unrecorded maybe — and sections 1–7 keep the work already done so
the next person re-costs the options instead of re-deriving them.

A call is priced by exactly two mechanisms, both from #181:

1. **The route's own reported charge**, carried verbatim. OpenRouter states `usage.cost` per request
   (`agent/provider_openai.go:535`), which is the exact number rather than an estimate.
2. **Rates the user declares** in `pricing.prices`, keyed `<route>/<model>`, in the lanes `Price` can
   express.

A call that neither mechanism can price is counted as **unknown, never as zero** (`CostUnknown`), so the
absence of a table is visible in the status bar instead of being silently absorbed into the total.

The second half matters more than the first: **the declared-price schema cannot express
what the provider this repository ships by default charges.** `Price` has five lanes and no time
dimension, while DeepSeek bills by hour. Until the schema can hold a time window, *any* number for our
own default route is wrong most of the time — vendored and declared alike. That work is
[#225](https://github.com/yusiwen/TinyCode/issues/225). This document is the decision, not the change.

*(That paragraph was written on 2026-10-14, when it was true. Since 2026-10-18 a declared rate can carry a
window, so the schema can hold what DeepSeek charges; the verdict — no vendored table — is what the date
above governs.)*

## 1. Source

**None.** No dataset is vendored, embedded or fetched, so there is no chosen source to name — and the
measurement below is why the two candidates that would have been chosen from were rejected for now.

| Candidate | What it carries | Read 2026-10-14 |
| :--- | :--- | :--- |
| [LiteLLM `model_prices_and_context_window.json`](https://docs.litellm.ai/docs/provider_registration/add_model_pricing) | 4,084 entries; per-lane costs; `input_cost_per_token_cache_hit`; `*_above_*k_tokens` and `*_batches` tiers; **`off_peak_pricing` with UTC windows** on 10 entries; `tiered_pricing` on 51 | 2.8 MB |
| [models.dev `api.json`](https://github.com/anomalyco/models.dev) | 229 providers, 8,832 models; cost per lane; `tiers` on 643 models and `context_over_200k` on 529, but **no time dimension anywhere** | 5.6 MB |
| Aggregator pricing feeds | `{inputPerM, cachedInputPerM, outputPerM}` and similar | not fetched |
| The providers themselves | a reported charge (OpenRouter) or tokens only (everyone else) | in use, via #181 |

**The disqualifying measurement: the two leading open datasets disagree about our own default route.**
For `deepseek-flash` input, LiteLLM's base entry says $0.30/M and models.dev says $0.15/M. Both are
right — LiteLLM's base entry is the *peak* rate, and its `off_peak_pricing` block holds the other half,
while models.dev carries the *off-peak* rate as its only number. A vendored table is therefore not merely
stale by construction; for the provider we ship it is **ambiguous**, and which value is correct depends
on the hour the call was made. Shipping either one alone would produce a confidently wrong number, which
is the failure this decision exists to avoid.

The providers themselves remain the best source, and they are the two mechanisms in the verdict: a
reported charge needs no table at all, and a declared rate is the user's own invoice.

## 2. Licence, attribution and provenance

Not applicable today, because nothing is vendored. Recorded now so a future reversal starts from the
facts rather than re-deriving them:

- **LiteLLM's data file is MIT**, but the repository is a *split* licence: its `LICENSE` grants MIT to
  "content outside" the `enterprise/` directory and a different licence inside it. GitHub's API therefore
  reports the repository as `NOASSERTION`. The consequence for a future pin is concrete: **pin the file
  and a commit SHA, not the repository**, and ship the notice naming which directory the MIT grant
  covers. The repository is also 2.3 GB, so a vendored copy must be a generated subset, never a subtree.
- **models.dev is MIT**, cleanly (`repos/anomalyco/models.dev` → `license=MIT`, 7.2k stars, pushed the
  day this was read). Attribution is still required.
- `as_of` is not part of any schema here, because no entry carries one: a declared rate is the user's own
  number and needs no provenance, and a reported charge is dated by the call it belongs to.

## 3. Update mechanism

None exists, because nothing is vendored. If this verdict is ever reversed, the mechanism is a
requirement before the data lands, not after:

- a generator committed with the repository — there is no `tools/` or `scripts/` directory today, so
  creating one is part of that work — which fetches a **pinned commit** (never a moving branch) and
  writes a **subset**, only the routes this repository can actually talk to, to a file marked
  `// Code generated … DO NOT EDIT.`;
- the generator is the only writer; a hand edit to the generated file is a defect;
- the diff is reviewed as a data change, with the licence and `as_of` re-checked in the same pull
  request, because a data refresh is the one change whose review cannot be inferred from the code.

## 4. Cadence and staleness policy

The policy that applies today is the one that makes a cadence unnecessary:

- a **declared** rate belongs to the user, so it carries no `as_of` and is labelled `declared` wherever
  it is displayed — an estimate from rates they typed, not a vendor fact;
- a **reported** charge is dated by the call itself and cannot go stale;
- a call nobody can price is `unknown`, and the count of such calls is displayed rather than guessed at.

If a table ever lands, its `as_of` is part of the entry schema and the rule is **withhold, then warn**:
past the freshness bound the rate is not used at all — the call is priced `unknown`, exactly as if no
rate had been declared — and past a warning bound it is used but marked stale. The direction is
deliberate: a stale number that still looks authoritative is worse than an absent one, because the user
cannot tell it apart from a right answer. A concrete bound would be chosen with the data (90 days is a
plausible start); naming one now, with no data in the tree, would be a guess dressed as policy.

## 5. Coverage and key

The key is `<route>/<model>`, with `*` allowed for either half and a fixed precedence — exact, then
`route/*`, then `*/model`, then `*/*` — so two entries can never both claim a call (`agent/pricing.go`).
The route is part of the key because a price belongs to a billing endpoint: the same model costs
differently through a reseller, a gateway and the vendor's own API. A model name alone is not a key.

Two properties keep this honest for data that may not exist:

- **An unmatched key is `unknown`, not zero.** A table that does not cover a route produces a visible
  "nobody could price this" count, which is the correct failure mode for a missing row.
- **Currency is part of the accumulation, not of the number.** `cost_currency` names the unit the user's
  bill is in; totals are kept per unit and never added across units, because a route may bill in credits
  rather than a currency (issue #181).

Filling the key is where a table would meet reality, and the awkwardness is already visible: until
[#226](https://github.com/yusiwen/TinyCode/issues/226) this repository's **default** sent the retired alias
`deepseek-v4-flash`, while the canonical name is `deepseek-flash`, and LiteLLM lists
`deepseek/deepseek-v4-flash` and `deepseek/deepseek-flash` as separate entries at the same rates. The
default, the shipped example and its price key all name `deepseek-flash` now. The alias still resolves for
a user who typed it into their own config, so alias resolution would be part of any vendored table's
coverage, not an edge case: a table that names only one of the two silently fails to price the other.

## 6. Tiering

This is the section that decides the verdict.

DeepSeek — the default provider — bills two input lanes, cache hit and cache miss, plus one output lane,
and each lane has a peak rate and an off-peak rate
([pricing](https://api-docs.deepseek.com/quick_start/pricing), read 2026-10-14):

| `deepseek-flash`, per 1M tokens | off-peak | peak |
| :--- | ---: | ---: |
| input, cache hit | $0.003 | $0.006 |
| input, cache miss | $0.15 | $0.30 |
| output | $0.60 | $1.20 |

Off-peak is exactly half of peak. Peak is **01:00–04:00 and 06:00–10:00 UTC, Monday to Friday, excluding
Chinese public holidays**; every other hour is off-peak, including weekends in full. That is **35 of 168
hours — 20.8% peak, 79.2% off-peak**.

What each side can express:

| | Time-of-day tier | Context-length tier | Batch rate |
| :--- | :--- | :--- | :--- |
| LiteLLM | **yes** — `off_peak_pricing` with `hours_utc`/`weekdays` | yes (`*_above_*k_tokens`) | yes (`*_batches`) |
| models.dev | **no** — one number per lane, and it is the off-peak one | yes (`context_over_200k`, `tiers`) | no |
| `agent.Price` (#181; windows since #225) | **yes** — a per-entry schedule of weekdays and UTC hours, plus the entry's own rate outside it | no | no |

LiteLLM's windows for `deepseek/deepseek-flash` are `00:00-01:00`, `04:00-06:00` and `10:00-00:00` on
weekdays plus all weekend — the complement of the vendor's peak rule, which is the right shape, and the
shape #225 adopted (an entry's own rates apply outside its windows, so the smaller of the two declarations
is the one to write). It does not encode Chinese public holidays, so it prices those as peak, and it would
inherit that approximation along with everything else a vendored table costs to maintain.

**Before #225, and the record of it.** `config.example.json` declared `deepseek/deepseek-flash` at
`0.30 / 1.20 / 0.006` — the *peak* rates — because `Price` had nowhere to put a second set. So the example's
estimate was **2× too high for about 79% of the week**, and nothing in the display said so; its own
`$comment` said so after #226, and the number still could not be right. Declaring the off-peak rates
instead was 2× too low during peak. There was no correct single number, which is precisely why the answer
to "should we vendor a table" could not be "yes" before the schema could hold what the table would say.

The example now declares the off-peak rates on the entry and both peak blocks as windows, and a test in the
repository's root package prices it at a fixed peak instant and a fixed off-peak one (`pricing_config_test.go`)
— the file is an executable part of this document, not a copy of it.

The choice is therefore between extending the schema and refusing to price such routes. This repository
extends the schema — [#225](https://github.com/yusiwen/TinyCode/issues/225), delivered 2026-10-18 — because
the user-declared path is the one that stays correct for contract prices, and it was worth nothing for the
default provider until it could express an hour. Until #225 landed, the honest reading of a DeepSeek
estimate was "somewhere between the two rates"; what remains of that sentence is the weekday holiday, which
the schema prices at peak (2× too high on those hours) because it does not carry a holiday calendar.

## 7. The rejected options, and why

- **Embed at build time** (`go:embed`, `as_of`, optional override). Rejected *for now*, and it was the
  closest call. It is offline, testable and reproducible, which fits a purely local accounting feature —
  but it needs a schema that can hold the tier (section 6), a generator and licence review (sections 2–3),
  and it would still be stale by construction for every other vendor. Reversing this needed #225 first, and
  #225 is in place (2026-10-18): what remains is the pipeline itself — a generator, a licence review and a
  refresh policy — which is a decision to take on its own merits.
- **Fetch at runtime** from a catalogue. Rejected as a mechanism. It adds a network dependency to *local
  accounting* — a feature whose whole job is to report what this machine did — and it buys a question
  that has no good answer: which catalogue wins, what happens when it is unreachable, and how a feed is
  treated as untrusted input. A run that cannot reach the network must still be able to price itself.
- **Stop at #181** — declared rates plus reported charges, with no schema work. Rejected, and this is the
  half of the issue that the shipped example settles: the default provider's price is time-dependent, so
  "no table for now" is not the same as "nothing to do". Leaving `Price` without a time dimension means
  the user's *own* declaration cannot be right either. What is settled is that a table is not the next
  step; accuracy is.
- **Vendor nothing and warn in the UI** that a declared rate may be tiered. Rejected as insufficient: a
  warning on every line is noise, and the example's 2× error would remain for anyone who never reads it.

## What would settle this

The question is left open deliberately, and it is documented rather than unanswered: it is worth taking
up again when any of these becomes true:

1. ~~#225 lands, so a declared rate can express an hour.~~ **Happened on 2026-10-18.** Re-costing the embed
   option is therefore open now rather than blocked: its only blocker was the schema.
2. A second default-or-common route needs a tier the schema still cannot express, which turns a one-off
   into a pattern.
3. The leading datasets stop disagreeing for a route we ship, which removes the ambiguity in section 1.
4. A user cannot price a route they use — the practical signal that declared rates are not enough.

## What this hands over

- [#225](https://github.com/yusiwen/TinyCode/issues/225) — a time-window tier for declared prices. The
  piece this verdict says has to exist before a table could be right for the default provider.
  **Delivered 2026-10-18**: the table holds a schedule per entry and resolves it at the instant the
  provider call started, the shipped example declares DeepSeek's real pair, and a malformed schedule is a
  startup error naming the window. Chinese public holidays stay unencoded; the example's `$comment` says
  which hours that makes 2× too high.
- [#226](https://github.com/yusiwen/TinyCode/issues/226) — the default model name is a retired alias;
  found while checking whether a vendored key would even match what this repository sends.

No application code lands from issue #182, and none lands here: this document and the two issues are its
deliverable.
