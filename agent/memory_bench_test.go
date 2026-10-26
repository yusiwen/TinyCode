package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// benchFact and benchQuery are the corpus file's shape (testdata/memory-bench.json).
type benchFact struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type benchQuery struct {
	Q    string `json:"q"`
	Want string `json:"want"`
	Kind string `json:"kind"` // lexical | paraphrase | crosslingual
}

type benchCorpus struct {
	Facts   []benchFact  `json:"facts"`
	Queries []benchQuery `json:"queries"`
}

// The corpus's documented shape. docs/memory-bench.md quotes these three
// numbers, so they are asserted here rather than left to drift with an edit.
const (
	benchFactCount        = 24
	benchQueryCount       = 20
	benchLexicalQueries   = 10
	benchParaphraseQuery  = 6
	benchCrosslingualQry  = 4
	benchRecallDepth      = 10
	benchLexicalFloorRank = 3
)

// loadBenchCorpus reads the fixed corpus the benchmark runs on.
func loadBenchCorpus(t *testing.T) benchCorpus {
	t.Helper()
	raw, err := os.ReadFile("testdata/memory-bench.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var corpus benchCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	if len(corpus.Facts) == 0 || len(corpus.Queries) == 0 {
		t.Fatal("the corpus is empty; a benchmark that runs on nothing passes vacuously")
	}
	return corpus
}

// TestMemoryBenchCorpusIsWellFormed guards the benchmark's own hygiene. Two
// properties make its columns mean what the doc says they mean: every query
// names a fact that exists, and a paraphrase or cross-lingual query shares no
// token with the fact it wants — if it did, the lexical ranker could answer it
// and the column would stop measuring what it claims to.
func TestMemoryBenchCorpusIsWellFormed(t *testing.T) {
	corpus := loadBenchCorpus(t)

	if len(corpus.Facts) != benchFactCount {
		t.Errorf("corpus has %d facts, docs/memory-bench.md says %d", len(corpus.Facts), benchFactCount)
	}
	if len(corpus.Queries) != benchQueryCount {
		t.Errorf("corpus has %d queries, docs/memory-bench.md says %d", len(corpus.Queries), benchQueryCount)
	}
	byKind := map[string]int{}
	facts := map[string]benchFact{}
	for _, fact := range corpus.Facts {
		if fact.Key == "" || fact.Value == "" {
			t.Errorf("a fact is missing its key or value: %+v", fact)
		}
		if _, dup := facts[fact.Key]; dup {
			t.Errorf("two facts share the key %q, so a rank cannot say which was found", fact.Key)
		}
		facts[fact.Key] = fact
	}
	for _, query := range corpus.Queries {
		byKind[query.Kind]++
		target, ok := facts[query.Want]
		if !ok {
			t.Errorf("query %q wants %q, which is not in the corpus", query.Q, query.Want)
			continue
		}
		shared := sharedTerms(query.Q, target.Key+" "+target.Value)
		switch query.Kind {
		case "lexical":
			if len(shared) == 0 {
				t.Errorf("lexical query %q shares no token with %q, so it is mislabelled", query.Q, query.Want)
			}
		case "paraphrase", "crosslingual":
			if len(shared) > 0 {
				t.Errorf("%s query %q shares %v with %q; the lexical ranker could answer it", query.Kind, query.Q, shared, query.Want)
			}
		default:
			t.Errorf("query %q has unknown kind %q", query.Q, query.Kind)
		}
	}
	if byKind["lexical"] != benchLexicalQueries {
		t.Errorf("corpus has %d lexical queries, docs/memory-bench.md says %d", byKind["lexical"], benchLexicalQueries)
	}
	if byKind["paraphrase"] != benchParaphraseQuery {
		t.Errorf("corpus has %d paraphrase queries, docs/memory-bench.md says %d", byKind["paraphrase"], benchParaphraseQuery)
	}
	if byKind["crosslingual"] != benchCrosslingualQry {
		t.Errorf("corpus has %d cross-lingual queries, docs/memory-bench.md says %d", byKind["crosslingual"], benchCrosslingualQry)
	}
}

// TestMemoryBenchLexicalFloor is the CI half of the benchmark: the local
// ranker must find a fact for a query that names one of its words, in the top
// three. It is deterministic — same corpus, same store — so it can be a gate
// rather than a report.
func TestMemoryBenchLexicalFloor(t *testing.T) {
	corpus := loadBenchCorpus(t)
	store := newTestStore(t, t.TempDir(), "bench")
	for _, fact := range corpus.Facts {
		if err := store.Remember(fact.Key, fact.Value); err != nil {
			t.Fatalf("remember %q: %v", fact.Key, err)
		}
	}

	ranks := benchRanks(t, store, corpus)
	for i, query := range corpus.Queries {
		if query.Kind != "lexical" {
			continue
		}
		if rank := ranks[i]; rank == 0 || rank > benchLexicalFloorRank {
			t.Errorf("lexical query %q ranked its fact at %d (want <= %d)", query.Q, rank, benchLexicalFloorRank)
		}
	}
	reportBench(t, "lexical", corpus, ranks)
}

// TestMemoryBenchMetrics pins the arithmetic the report is made of, on rankings
// with a known answer, so a wrong hit@k cannot look like a result.
func TestMemoryBenchMetrics(t *testing.T) {
	m := benchMetricsFor([]int{1, 2, 3, 7, 0})
	if m.N != 5 {
		t.Fatalf("N = %d", m.N)
	}
	if m.Hit1 != 1 || m.Hit3 != 3 || m.Hit5 != 3 {
		t.Fatalf("hits = %d/%d/%d, want 1/3/3 (rank 7 is outside the top five)", m.Hit1, m.Hit3, m.Hit5)
	}
	wantMRR := (1.0 + 1.0/2 + 1.0/3 + 1.0/7 + 0) / 5
	if diff := m.MRR - wantMRR; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("MRR = %.6f, want %.6f", m.MRR, wantMRR)
	}
	if empty := benchMetricsFor(nil); empty.N != 0 || empty.MRR != 0 {
		t.Fatalf("an empty ranking produced %+v", empty)
	}
}

// TestMemoryBenchRealEndpoint measures against a real model. It is gated: a
// benchmark that silently runs against a fake reports the fake's opinions, and
// a benchmark that fails without an endpoint is a gate nobody can keep.
//
//	TINYCODE_MEMORY_BENCH_URL=http://127.0.0.1:11434 \
//	TINYCODE_MEMORY_BENCH_MODEL=nomic-embed-text \
//	go test -count=1 -v -run TestMemoryBenchRealEndpoint ./agent/
func TestMemoryBenchRealEndpoint(t *testing.T) {
	base := os.Getenv("TINYCODE_MEMORY_BENCH_URL")
	model := os.Getenv("TINYCODE_MEMORY_BENCH_MODEL")
	if base == "" || model == "" {
		t.Skip("set TINYCODE_MEMORY_BENCH_URL and TINYCODE_MEMORY_BENCH_MODEL to measure against a real embedding endpoint")
	}
	var embedder Embedder
	if strings.EqualFold(os.Getenv("TINYCODE_MEMORY_BENCH_PROVIDER"), "openai") {
		embedder = NewOpenAIEmbedder(base, model, os.Getenv("TINYCODE_MEMORY_BENCH_API_KEY"))
	} else {
		embedder = NewOllamaEmbedder(base, model)
	}

	corpus := loadBenchCorpus(t)
	inner := newTestStore(t, t.TempDir(), "bench")
	semantic, err := NewSemanticMemory(inner, embedder, SemanticOptions{})
	if err != nil {
		t.Fatalf("open semantic store: %v", err)
	}
	for _, fact := range corpus.Facts {
		if err := semantic.Remember(fact.Key, fact.Value); err != nil {
			t.Fatalf("remember %q: %v", fact.Key, err)
		}
	}
	semantic.mu.Lock()
	embedded := len(semantic.vectors)
	semantic.mu.Unlock()
	if embedded == 0 {
		t.Fatalf("the endpoint at %s returned no vectors for %q; recall would silently fall back to lexical", base, model)
	}
	if embedded != len(corpus.Facts) {
		t.Fatalf("%d of %d facts were embedded; a partial corpus misreports every column", embedded, len(corpus.Facts))
	}

	t.Logf("model=%s endpoint=%s facts=%d queries=%d", model, base, len(corpus.Facts), len(corpus.Queries))
	reportBench(t, "lexical", corpus, benchRanks(t, inner, corpus))
	reportBench(t, "semantic", corpus, benchRanks(t, semantic, corpus))
}

// benchRanks returns, per query, the 1-based rank of the wanted fact in a
// recall of benchRecallDepth, or 0 when it is not there.
func benchRanks(t *testing.T, store types.MemoryStore, corpus benchCorpus) []int {
	t.Helper()
	ranks := make([]int, len(corpus.Queries))
	for i, query := range corpus.Queries {
		hits, err := store.Recall(context.Background(), query.Q, benchRecallDepth)
		if err != nil {
			t.Fatalf("recall %q: %v", query.Q, err)
		}
		for position, hit := range hits {
			if hit.Key == query.Want {
				ranks[i] = position + 1
				break
			}
		}
	}
	return ranks
}

// benchMetrics is one ranker's score over one group of queries.
type benchMetrics struct {
	N, Hit1, Hit3, Hit5 int
	MRR                 float64
}

func benchMetricsFor(ranks []int) benchMetrics {
	m := benchMetrics{N: len(ranks)}
	var reciprocal float64
	for _, rank := range ranks {
		switch {
		case rank == 1:
			m.Hit1++
			m.Hit3++
			m.Hit5++
		case rank == 2:
			m.Hit3++
			m.Hit5++
		case rank == 3:
			m.Hit3++
			m.Hit5++
		case rank > 3 && rank <= 5:
			m.Hit5++
		}
		if rank > 0 {
			reciprocal += 1 / float64(rank)
		}
	}
	if m.N > 0 {
		m.MRR = reciprocal / float64(m.N)
	}
	return m
}

// reportBench prints one line per (ranker, kind) in both a readable table and a
// `MEMBENCH ...` form that is easy to quote in docs/memory-bench.md.
func reportBench(t *testing.T, ranker string, corpus benchCorpus, ranks []int) {
	t.Helper()
	kinds := []string{"lexical", "paraphrase", "crosslingual"}
	for _, kind := range kinds {
		var group []int
		for i, query := range corpus.Queries {
			if query.Kind == kind {
				group = append(group, ranks[i])
			}
		}
		m := benchMetricsFor(group)
		if m.N == 0 {
			continue
		}
		t.Logf("MEMBENCH ranker=%-8s kind=%-12s n=%-2d hit1=%.2f hit3=%.2f hit5=%.2f mrr=%.2f",
			ranker, kind, m.N, float64(m.Hit1)/float64(m.N), float64(m.Hit3)/float64(m.N),
			float64(m.Hit5)/float64(m.N), m.MRR)
		for i, query := range corpus.Queries {
			if query.Kind == kind && ranks[i] == 0 {
				t.Logf("  miss: %s -> %s", query.Q, query.Want)
			}
		}
	}
}

// sharedTerms is the hygiene check's ruler: the tokens two texts have in common,
// sorted so a failure message reads the same every run.
func sharedTerms(a, b string) []string {
	inB := map[string]bool{}
	for _, term := range memoryTerms(b) {
		inB[term] = true
	}
	var shared []string
	seen := map[string]bool{}
	for _, term := range memoryTerms(a) {
		if inB[term] && !seen[term] {
			seen[term] = true
			shared = append(shared, term)
		}
	}
	sort.Strings(shared)
	return shared
}

// String keeps the metrics readable in a failure message.
func (m benchMetrics) String() string {
	return fmt.Sprintf("n=%d hit1=%d hit3=%d hit5=%d mrr=%.2f", m.N, m.Hit1, m.Hit3, m.Hit5, m.MRR)
}
