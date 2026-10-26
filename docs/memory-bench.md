# Memory recall benchmark

Recall was a keyword match until roadmap B3 added vector recall, and "semantic beats lexical" is a
claim, not a measurement. This is the instrument: a fixed corpus, a fixed query set, and four numbers
per group. It exists so the next decision about memory — consolidation, quantisation, a cross-encoder,
a different model — is made against a number instead of an impression.

## The corpus

`agent/testdata/memory-bench.json` holds **24 facts** and **20 queries**, the counts a test asserts
against this page. Facts are what a person would actually `/remember` about a repository (a test
command, a staging database address, a release rule). Every query names the fact it should bring back,
and each carries one of three kinds:

| Kind | Queries | Property |
| :--- | ---: | :--- |
| `lexical` | 10 | shares a distinctive word with its fact — what the local ranker is for |
| `paraphrase` | 6 | shares **no** token with its fact, and says the same thing — the case embeddings exist for |
| `crosslingual` | 4 | Chinese query, English fact — the same case one language apart |

The hygiene rule is a gate, not a convention: `TestMemoryBenchCorpusIsWellFormed` fails when a
`paraphrase` or `crosslingual` query shares a token with its target (the lexical ranker could then
answer it and the column would stop measuring what it says), or when a `lexical` query shares none.

## The numbers

Ranked at depth 10, per group: `hit@1`, `hit@3`, `hit@5` (the wanted fact is in the top k) and `MRR`
(mean reciprocal rank, 0 for a miss).

Measured **2026-10-26** against a local Ollama 0.40.11 (`http://127.0.0.1:11434`), each model run twice
and byte-identical both times. The lexical rows do not involve a model at all — they are the baseline
the semantic rows are read against.

| Ranker | Model (dims) | Group | n | hit@1 | hit@3 | hit@5 | MRR |
| :--- | :--- | :--- | ---: | ---: | ---: | ---: | ---: |
| lexical | — | lexical | 10 | 1.00 | 1.00 | 1.00 | 1.00 |
| lexical | — | paraphrase | 6 | 0.00 | 0.00 | 0.00 | 0.00 |
| lexical | — | crosslingual | 4 | 0.00 | 0.00 | 0.00 | 0.00 |
| semantic | nomic-embed-text (768) | lexical | 10 | 1.00 | 1.00 | 1.00 | 1.00 |
| semantic | nomic-embed-text (768) | paraphrase | 6 | 0.33 | 0.67 | 0.83 | 0.49 |
| semantic | nomic-embed-text (768) | crosslingual | 4 | 0.00 | 0.25 | 0.25 | 0.08 |
| semantic | bge-m3 (1024) | lexical | 10 | 1.00 | 1.00 | 1.00 | 1.00 |
| semantic | bge-m3 (1024) | paraphrase | 6 | 0.50 | 0.50 | 0.83 | 0.61 |
| semantic | bge-m3 (1024) | crosslingual | 4 | 0.75 | 1.00 | 1.00 | 0.83 |

What the numbers say:

- **The premise holds for wording.** Lexical recall answers none of the six paraphrases. Either model
  answers most of them within five, and the two differ by one query at hit@1 (two of six vs three).
- **Cross-lingual recall is the model, not the store.** `nomic-embed-text` finds one of four Chinese
  queries in the top three; `bge-m3` finds all four and three of them first. Same store, same corpus,
  same code — the difference is entirely which model produced the vectors.
- **Nothing was traded away.** Both models keep hit@1 at 1.00 on the queries that share a word. A
  semantic model that lost the easy case would be a regression wearing a feature's name.
- **The corpus is small.** One query is 17 points in the paraphrase column and 25 in the cross-lingual
  one. Read the ordering, not the decimals.

The `MEMBENCH` lines behind the table, verbatim:

```
model=nomic-embed-text endpoint=http://127.0.0.1:11434 dims=768 facts=24 queries=20
ranker=semantic kind=lexical      n=10 hit1=1.00 hit3=1.00 hit5=1.00 mrr=1.00
ranker=semantic kind=paraphrase   n=6  hit1=0.33 hit3=0.67 hit5=0.83 mrr=0.49
ranker=semantic kind=crosslingual n=4  hit1=0.00 hit3=0.25 hit5=0.25 mrr=0.08

model=bge-m3 endpoint=http://127.0.0.1:11434 dims=1024 facts=24 queries=20
ranker=semantic kind=lexical      n=10 hit1=1.00 hit3=1.00 hit5=1.00 mrr=1.00
ranker=semantic kind=paraphrase   n=6  hit1=0.50 hit3=0.50 hit5=0.83 mrr=0.61
ranker=semantic kind=crosslingual n=4  hit1=0.75 hit3=1.00 hit5=1.00 mrr=0.83
```

Both models were installed locally with Ollama (`nomic-embed-text` 274 MB, `bge-m3` 1.2 GB) and the run
took seconds; the corpus is small enough that a laptop embeds it faster than the page took to write.

## Running it

```bash
# Local Ollama (default provider)
make memory-bench MEMORY_BENCH_URL=http://127.0.0.1:11434 MEMORY_BENCH_MODEL=nomic-embed-text

# Any OpenAI-compatible /embeddings endpoint
make memory-bench MEMORY_BENCH_PROVIDER=openai MEMORY_BENCH_URL=https://api.openai.com/v1 \
  MEMORY_BENCH_MODEL=text-embedding-3-small MEMORY_BENCH_API_KEY=sk-...
```

The target runs `TestMemoryBenchRealEndpoint`, which embeds the whole corpus, refuses to report if any
fact failed to embed, then prints two `MEMBENCH` blocks — one for the local ranker, one for the
semantic one — with a `miss:` line under each that did not land.

## Reading it

- **Compare within a model, never across.** Two models produce vectors that are not comparable, and the
  corpus is small enough that one query is 17 points in the `paraphrase` column.
- **The `crosslingual` column is a model property.** An English-only embedding model scores about zero
  there however good the store is; use a multilingual one (`bge-m3`, `multilingual-e5`) before
  concluding anything about cross-lingual recall.
- **The lexical column is a floor, not a ceiling.** A model that loses `hit@3` there is worse than the
  thing it replaced, and `TestMemoryBenchLexicalFloor` already holds the local ranker to that.
- **The corpus is a sample of one person's memory.** It is a regression instrument and a sanity check,
  not a benchmark against which to publish a model.

## What CI runs

- `TestMemoryBenchCorpusIsWellFormed` — the documented counts and the hygiene rule above.
- `TestMemoryBenchLexicalFloor` — every `lexical` query finds its fact in the top 3, on the local
  ranker. Deterministic, so it is a gate.
- `TestMemoryBenchMetrics` — the hit/MRR arithmetic, on rankings with a known answer.
- `TestMemoryBenchRealEndpoint` — **skips** without `TINYCODE_MEMORY_BENCH_URL` and
  `TINYCODE_MEMORY_BENCH_MODEL`. CI has no model, so quality is measured by a person, on demand, with
  the command above.
