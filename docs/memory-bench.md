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

Baseline, measured on this machine with the local ranker only — deterministic, so these are exact
rather than a sample:

```
ranker=lexical  kind=lexical      n=10 hit1=1.00 hit3=1.00 hit5=1.00 mrr=1.00
ranker=lexical  kind=paraphrase   n=6  hit1=0.00 hit3=0.00 hit5=0.00 mrr=0.00
ranker=lexical  kind=crosslingual n=4  hit1=0.00 hit3=0.00 hit5=0.00 mrr=0.00
```

That is the shape of the gap B3 was opened for: perfect where a word is shared, nothing where the
words differ.

**A semantic model's numbers are not recorded here yet.** No embedding endpoint was configured when
this page was written, and a number nobody ran is worse than an empty cell. Run the command below and
put its `MEMBENCH` lines here with the model's name, the date and the endpoint.

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
