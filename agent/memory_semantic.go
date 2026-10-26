package agent

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// vectorRecord is one line of the sidecar file: a fact's vector and the model
// that produced it. The vector travels as base64 of little-endian float32s; a
// JSON array of floats would be three times the size for a file nobody reads.
type vectorRecord struct {
	ID    string `json:"id"`
	Model string `json:"model"`
	Dim   int    `json:"dim"`
	V     string `json:"v"`
}

// SemanticOptions are the knobs the config resolves for vector recall.
type SemanticOptions struct {
	MinScore       float64
	SemanticWeight float64
	LexicalWeight  float64
	Backfill       int
}

// Defaults for the vector recall knobs. The config layer carries the same
// numbers and a test asserts the pair equal.
const (
	defaultSemanticMinScore = 0.25
	defaultSemanticWeight   = 0.7
	defaultLexicalWeight    = 0.3
	defaultBackfill         = 16
)

// embedTimeout bounds one embedding call made outside a run's context: a fact
// typed at the prompt must not hang the prompt.
const embedTimeout = 5 * time.Second

// SemanticMemory adds vector recall to the local store (roadmap B3).
//
// The facts stay where they were — the JSONL the local store owns — and the
// vectors live in a sidecar keyed by fact id and model. That split is deliberate:
// the text file keeps its bound and stays readable, and vectors from a model that
// is no longer configured can be dropped without touching a fact.
type SemanticMemory struct {
	inner    *LocalMemory
	embedder Embedder
	path     string

	minScore      float64
	semW          float64
	lexW          float64
	backfillBatch int

	mu      sync.Mutex
	vectors map[string][]float32
}

// NewSemanticMemory wraps a local store with vector recall. The sidecar is
// created next to it, and vectors from a different model are ignored — vectors
// from two models are not comparable, so mixing them would rank facts by noise.
func NewSemanticMemory(inner *LocalMemory, embedder Embedder, opts SemanticOptions) (*SemanticMemory, error) {
	if inner == nil || embedder == nil {
		return nil, fmt.Errorf("memory: semantic recall needs a store and an embedder")
	}
	if opts.MinScore <= 0 {
		opts.MinScore = defaultSemanticMinScore
	}
	if opts.SemanticWeight <= 0 {
		opts.SemanticWeight = defaultSemanticWeight
	}
	if opts.LexicalWeight <= 0 {
		opts.LexicalWeight = defaultLexicalWeight
	}
	if opts.Backfill <= 0 {
		opts.Backfill = defaultBackfill
	}
	m := &SemanticMemory{
		inner:         inner,
		embedder:      embedder,
		path:          strings.TrimSuffix(inner.Path(), ".jsonl") + ".vec.jsonl",
		minScore:      opts.MinScore,
		semW:          opts.SemanticWeight,
		lexW:          opts.LexicalWeight,
		backfillBatch: opts.Backfill,
		vectors:       map[string][]float32{},
	}
	if err := m.loadVectors(); err != nil {
		return nil, err
	}
	return m, nil
}

// Path is the sidecar file, exported for tests and for the same reason the local
// store exports its own.
func (m *SemanticMemory) Path() string { return m.path }

// Remember stores the fact first and its vector second. A failed embedding does
// not lose the fact: the next recall backfills it, and until then the local
// store's lexical ranking still finds it.
func (m *SemanticMemory) Remember(key, value string) error {
	// Normalise exactly as the store does before deriving the id: a fact with no
	// label is stored as "fact", and a vector filed under the empty key would
	// never be found again.
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" && value != "" {
		key = "fact"
	}
	if err := m.inner.Remember(key, value); err != nil {
		return err
	}
	id := memoryID(key, value)
	ctx, cancel := context.WithTimeout(context.Background(), embedTimeout)
	defer cancel()
	vecs, err := m.embedder.Embed(ctx, []string{embedText(key, value)})
	if err != nil {
		tlog.Warn("agent.memory", "embed failed, fact kept", "error", err)
		return nil
	}
	if len(vecs) != 1 {
		tlog.Warn("agent.memory", "embed returned no vector", "count", len(vecs))
		return nil
	}
	if err := m.storeVector(id, vecs[0]); err != nil {
		tlog.Warn("agent.memory", "vector not saved", "error", err)
	}
	return nil
}

// Recall ranks by cosine, blends in the local store's lexical ranking, and falls
// back to that ranking alone if the endpoint is unavailable. The fallback is the
// point: a dead embedding server costs recall quality, never a run.
func (m *SemanticMemory) Recall(ctx context.Context, query string, limit int) ([]types.Memory, error) {
	if limit <= 0 {
		limit = DefaultMemoryRecallLimit
	}
	if strings.TrimSpace(query) == "" {
		return m.inner.Recall(ctx, query, limit)
	}
	if err := m.backfill(ctx); err != nil {
		tlog.Warn("agent.memory", "backfill incomplete", "error", err)
	}

	queryCtx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()
	vecs, err := m.embedder.Embed(queryCtx, []string{memoryQuery(query)})
	if err != nil || len(vecs) != 1 {
		tlog.Warn("agent.memory", "recall fell back to lexical ranking", "error", err)
		return m.inner.Recall(ctx, query, limit)
	}
	queryVec := normalize(vecs[0])

	facts, err := m.inner.List()
	if err != nil {
		return nil, err
	}
	lexical, err := m.inner.Recall(ctx, query, limit*2)
	if err != nil {
		return nil, err
	}
	lexRank := make(map[string]int, len(lexical))
	for i, fact := range lexical {
		lexRank[fact.ID] = i
	}

	type scored struct {
		fact  types.Memory
		score float64
	}
	m.mu.Lock()
	candidates := make([]scored, 0, len(facts))
	for _, fact := range facts {
		vec, hasVector := m.vectors[fact.ID]
		sim := 0.0
		if hasVector {
			sim = cosine(queryVec, vec)
		}
		rank, lexicalHit := lexRank[fact.ID]
		if !hasVector && !lexicalHit {
			continue
		}
		// A semantic candidate has to clear the floor; a lexical hit is already
		// evidence, so it stays in the pool either way.
		if hasVector && sim < m.minScore && !lexicalHit {
			continue
		}
		lexScore := 0.0
		if lexicalHit {
			lexScore = 1 / float64(rank+1)
		}
		weight := m.semW + m.lexW
		candidates = append(candidates, scored{
			fact:  fact,
			score: (m.semW*sim + m.lexW*lexScore) / weight,
		})
	}
	m.mu.Unlock()

	sort.SliceStable(candidates, func(a, b int) bool {
		if candidates[a].score != candidates[b].score {
			return candidates[a].score > candidates[b].score
		}
		return candidates[a].fact.CreatedAt.After(candidates[b].fact.CreatedAt)
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	out := make([]types.Memory, len(candidates))
	for i, c := range candidates {
		out[i] = c.fact
	}
	return out, nil
}

// List and Forget are the local store's, with the vector dropped alongside the
// fact so a later fact with the same text does not inherit a stale vector.
func (m *SemanticMemory) List() ([]types.Memory, error) { return m.inner.List() }

func (m *SemanticMemory) Forget(id string) error {
	if err := m.inner.Forget(id); err != nil {
		return err
	}
	m.mu.Lock()
	_, had := m.vectors[id]
	delete(m.vectors, id)
	m.mu.Unlock()
	if had {
		if err := m.rewriteVectors(); err != nil {
			tlog.Warn("agent.memory", "vector file not rewritten", "error", err)
		}
	}
	return nil
}

// backfill embeds facts that have no vector yet — written before embeddings were
// enabled, or written while the endpoint was down — in one bounded batch.
func (m *SemanticMemory) backfill(ctx context.Context) error {
	facts, err := m.inner.List()
	if err != nil {
		return err
	}
	m.mu.Lock()
	var pending []types.Memory
	for _, fact := range facts {
		if _, ok := m.vectors[fact.ID]; ok {
			continue
		}
		pending = append(pending, fact)
		if len(pending) >= m.backfillBatch {
			break
		}
	}
	m.mu.Unlock()
	if len(pending) == 0 {
		return nil
	}

	texts := make([]string, len(pending))
	for i, fact := range pending {
		texts[i] = embedText(fact.Key, fact.Value)
	}
	batchCtx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()
	vecs, err := m.embedder.Embed(batchCtx, texts)
	if err != nil {
		return err
	}
	if len(vecs) != len(pending) {
		return fmt.Errorf("embedding: asked for %d vectors, got %d", len(pending), len(vecs))
	}
	for i, fact := range pending {
		if err := m.storeVector(fact.ID, vecs[i]); err != nil {
			return err
		}
	}
	tlog.Debug("agent.memory", "backfilled", "count", len(pending))
	return nil
}

// storeVector normalises and appends one vector, and remembers it in memory.
func (m *SemanticMemory) storeVector(id string, vec []float32) error {
	if len(vec) == 0 {
		return fmt.Errorf("embedding: empty vector for %s", id)
	}
	norm := normalize(vec)
	record := vectorRecord{ID: id, Model: m.embedder.Model(), Dim: len(norm), V: encodeVector(norm)}
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("memory: encode vector: %w", err)
	}
	f, err := os.OpenFile(m.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open %s: %w", m.path, err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("memory: append %s: %w", m.path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("memory: close %s: %w", m.path, err)
	}
	m.mu.Lock()
	m.vectors[id] = norm
	m.mu.Unlock()
	return nil
}

// loadVectors reads the sidecar, keeping only this model's vectors. A file that
// held another model's vectors (or duplicates from a re-embed) is rewritten, so
// the stale lines do not accumulate.
func (m *SemanticMemory) loadVectors() error {
	f, err := os.Open(m.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memory: open %s: %w", m.path, err)
	}
	defer f.Close()

	model := m.embedder.Model()
	skipped := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record vectorRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			skipped++
			continue
		}
		if record.Model != model {
			skipped++
			continue
		}
		vec, err := decodeVector(record.V, record.Dim)
		if err != nil {
			skipped++
			continue
		}
		if _, dup := m.vectors[record.ID]; dup {
			skipped++
		}
		m.vectors[record.ID] = vec
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("memory: read %s: %w", m.path, err)
	}
	if skipped > 0 {
		if err := m.rewriteVectors(); err != nil {
			return err
		}
	}
	return nil
}

// rewriteVectors replaces the sidecar atomically with the vectors in memory.
func (m *SemanticMemory) rewriteVectors() error {
	m.mu.Lock()
	records := make([]vectorRecord, 0, len(m.vectors))
	for id, vec := range m.vectors {
		records = append(records, vectorRecord{ID: id, Model: m.embedder.Model(), Dim: len(vec), V: encodeVector(vec)})
	}
	m.mu.Unlock()
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })

	tmp, err := os.CreateTemp(filepath.Dir(m.path), filepath.Base(m.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("memory: temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	w := bufio.NewWriter(tmp)
	for _, record := range records {
		line, err := json.Marshal(record)
		if err != nil {
			tmp.Close()
			return fmt.Errorf("memory: encode vector: %w", err)
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			tmp.Close()
			return fmt.Errorf("memory: write %s: %w", name, err)
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return fmt.Errorf("memory: flush %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("memory: sync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("memory: close %s: %w", name, err)
	}
	if err := os.Rename(name, m.path); err != nil {
		return fmt.Errorf("memory: replace %s: %w", m.path, err)
	}
	return nil
}

// embedText is what a fact's vector is computed from: the label gives the model
// the topic, the value the content.
func embedText(key, value string) string {
	if strings.TrimSpace(key) == "" {
		return value
	}
	return key + ": " + value
}

// encodeVector packs unit-length float32s as base64 of little-endian words.
func encodeVector(vec []float32) string {
	buf := make([]byte, 4*len(vec))
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// decodeVector reads one back, checking the length against the recorded
// dimension so a truncated line is a skipped line rather than a shifted vector.
func decodeVector(encoded string, dim int) ([]float32, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("memory: decode vector: %w", err)
	}
	if len(raw) != dim*4 {
		return nil, fmt.Errorf("memory: vector is %d bytes, want %d", len(raw), dim*4)
	}
	vec := make([]float32, dim)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:]))
	}
	return vec, nil
}
