package agent

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yusiwen/tinycode/types"
)

// Bounds for the local store. The config layer carries the same three numbers
// and a test asserts the pair equal, so the two cannot drift.
const (
	defaultLocalMemoryMaxEntries = 2000
	defaultLocalMemoryMaxBytes   = 8 << 20
	defaultLocalMemoryRecall     = 5
)

// ErrMemoryNotFound reports a Forget for an id the store does not hold, so a
// caller can say so rather than claim a delete that did not happen.
var ErrMemoryNotFound = errors.New("memory not found")

// LocalMemory is the on-disk store behind recall: one JSON object per line, in
// the directory the config names, sharded by project so two repositories do not
// read each other's facts.
//
// Its only writer is the user's `/remember` (docs/memory-decision.md), so this
// type deliberately has no summariser and no hook that would run one. The file
// is the durable copy; entries is the in-memory view, oldest first, and it is
// rewritten whole when a bound is crossed or a fact is forgotten.
type LocalMemory struct {
	mu   sync.Mutex
	path string

	maxEntries int
	maxBytes   int64

	entries []types.Memory
	ids     map[string]struct{}
	// size is the encoded size of entries, kept in step with the file so the
	// byte bound costs one comparison rather than a re-encode of the store on
	// every write.
	size int64

	// now is injectable so a test can place two facts in a known order.
	now func() time.Time
}

// NewLocalMemory opens (creating if needed) the store for one scope. A scope is
// the shard name — MemoryScopeFor derives it from the project root — and an
// empty one reads as "global".
func NewLocalMemory(dir, scope string, maxEntries int, maxBytes int64) (*LocalMemory, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("memory: no store directory")
	}
	if scope == "" {
		scope = "global"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("memory: create %s: %w", dir, err)
	}
	m := &LocalMemory{
		path:       filepath.Join(dir, scope+".jsonl"),
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		ids:        map[string]struct{}{},
		now:        time.Now,
	}
	if m.maxEntries <= 0 {
		m.maxEntries = defaultLocalMemoryMaxEntries
	}
	if m.maxBytes <= 0 {
		m.maxBytes = defaultLocalMemoryMaxBytes
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// Path is the file this store keeps its facts in. It is exported for the
// composition root, which reserves the directory from the tool layer.
func (m *LocalMemory) Path() string { return m.path }

// MemoryScopeFor names the shard a project's facts belong to: a digest of the
// absolute project root, so the same repository finds the same file from any
// working directory and two repositories never share one.
func MemoryScopeFor(projectRoot string) string {
	root := strings.TrimSpace(projectRoot)
	if root == "" {
		return "global"
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	sum := sha256.Sum256([]byte(filepath.Clean(root)))
	return hex.EncodeToString(sum[:])[:12]
}

// Remember appends one fact. The id is derived from (key, value), so remembering
// the same fact twice is a no-op rather than a duplicate.
func (m *LocalMemory) Remember(key, value string) error {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("memory: empty value")
	}
	if key == "" {
		key = "fact"
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	id := memoryID(key, value)
	if _, exists := m.ids[id]; exists {
		return nil
	}
	entry := types.Memory{ID: id, Key: key, Value: value, Source: "user", CreatedAt: m.now().UTC()}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("memory: encode: %w", err)
	}
	if int64(len(line)+1) > m.maxBytes {
		// Refused rather than stored: a fact that cannot fit the bound would
		// otherwise evict everything else the store holds.
		return fmt.Errorf("memory: fact is %d bytes, past the %d-byte store bound", len(line), m.maxBytes)
	}
	if err := m.appendBytes(line); err != nil {
		return err
	}
	m.entries = append(m.entries, entry)
	m.ids[id] = struct{}{}
	m.size += int64(len(line) + 1)
	return m.trimLocked()
}

// Recall scores the store lexically and returns at most limit facts, best first.
// It is deliberately not a semantic match: embeddings are roadmap B3, and a
// keyword hit that says so is better than a similarity nobody measured.
func (m *LocalMemory) Recall(query string, limit int) ([]types.Memory, error) {
	terms := memoryTerms(query)
	if len(terms) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = defaultLocalMemoryRecall
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.entries) == 0 {
		return nil, nil
	}

	// Document frequency, and the term set of each entry, computed once per call.
	sets := make([]map[string]bool, len(m.entries))
	freq := make(map[string]int, len(m.entries))
	for i, entry := range m.entries {
		set := make(map[string]bool)
		for _, term := range memoryTerms(entry.Key + " " + entry.Value) {
			set[term] = true
		}
		sets[i] = set
		for term := range set {
			freq[term]++
		}
	}

	type hit struct {
		entry types.Memory
		score float64
		order int
	}
	hits := make([]hit, 0, len(m.entries))
	total := float64(len(m.entries))
	for i, entry := range m.entries {
		var score float64
		for _, term := range terms {
			if !sets[i][term] {
				continue
			}
			// Terms in fewer entries carry more of the fact's meaning; a term
			// every entry shares carries almost none.
			score += math.Log(1 + total/float64(freq[term]))
		}
		if score > 0 {
			hits = append(hits, hit{entry: entry, score: score, order: i})
		}
	}
	sort.SliceStable(hits, func(a, b int) bool {
		if hits[a].score != hits[b].score {
			return hits[a].score > hits[b].score
		}
		if !hits[a].entry.CreatedAt.Equal(hits[b].entry.CreatedAt) {
			return hits[a].entry.CreatedAt.After(hits[b].entry.CreatedAt)
		}
		return hits[a].order > hits[b].order
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]types.Memory, len(hits))
	for i, h := range hits {
		out[i] = h.entry
	}
	return out, nil
}

// List returns every stored fact, oldest first.
func (m *LocalMemory) List() ([]types.Memory, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]types.Memory, len(m.entries))
	copy(out, m.entries)
	return out, nil
}

// Forget removes one fact by id and rewrites the file without it. A real delete
// is the point: the remote adapter's no-op is not the model here.
func (m *LocalMemory) Forget(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrMemoryNotFound
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, entry := range m.entries {
		if entry.ID != id {
			continue
		}
		m.entries = append(m.entries[:i], m.entries[i+1:]...)
		delete(m.ids, id)
		return m.rewriteLocked()
	}
	return ErrMemoryNotFound
}

// load reads the file into memory. A line that cannot be decoded is skipped
// rather than costing every other fact in the store.
func (m *LocalMemory) load() error {
	f, err := os.Open(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memory: open %s: %w", m.path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry types.Memory
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if entry.ID == "" {
			entry.ID = memoryID(entry.Key, entry.Value)
		}
		if _, dup := m.ids[entry.ID]; dup {
			continue
		}
		m.ids[entry.ID] = struct{}{}
		m.entries = append(m.entries, entry)
		m.size += int64(len(line) + 1)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("memory: read %s: %w", m.path, err)
	}
	return m.trimLocked()
}

// appendBytes is the only write that does not rewrite the file. One line, one
// O_APPEND write: that is the whole of the cross-process guarantee (see issue
// #293's out-of-scope note on locking).
func (m *LocalMemory) appendBytes(line []byte) error {
	f, err := os.OpenFile(m.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("memory: open %s: %w", m.path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("memory: append %s: %w", m.path, err)
	}
	return nil
}

// trimLocked drops the oldest facts until both bounds hold, and rewrites the
// file only when it actually dropped something.
func (m *LocalMemory) trimLocked() error {
	if len(m.entries) <= m.maxEntries && m.size <= m.maxBytes {
		return nil
	}
	for len(m.entries) > 0 && (len(m.entries) > m.maxEntries || m.size > m.maxBytes) {
		dropped := m.entries[0]
		m.size -= int64(encodedLen(dropped))
		m.entries = m.entries[1:]
		delete(m.ids, dropped.ID)
	}
	return m.rewriteLocked()
}

// rewriteLocked replaces the file atomically: a temp file in the same directory,
// fsynced, then renamed over the original, so a crash leaves either the old
// store or the new one.
func (m *LocalMemory) rewriteLocked() error {
	tmp, err := os.CreateTemp(filepath.Dir(m.path), filepath.Base(m.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("memory: temp file: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)

	w := bufio.NewWriter(tmp)
	var written int64
	for _, entry := range m.entries {
		line, err := json.Marshal(entry)
		if err != nil {
			_ = tmp.Close()
			return fmt.Errorf("memory: encode: %w", err)
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("memory: write %s: %w", name, err)
		}
		written += int64(len(line) + 1)
	}
	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("memory: flush %s: %w", name, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("memory: sync %s: %w", name, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("memory: close %s: %w", name, err)
	}
	if err := os.Rename(name, m.path); err != nil {
		return fmt.Errorf("memory: replace %s: %w", m.path, err)
	}
	m.size = written
	return nil
}

func encodedLen(entry types.Memory) int {
	line, err := json.Marshal(entry)
	if err != nil {
		return 0
	}
	return len(line) + 1
}

// memoryID derives an id from the fact itself, so the same (key, value) pair is
// the same fact however many times it is remembered.
func memoryID(key, value string) string {
	sum := sha256.Sum256([]byte(key + "\x00" + value))
	return hex.EncodeToString(sum[:])[:16]
}

// memoryTerms tokenises text for lexical recall: ASCII words of two characters
// or more, and — because those scripts do not put spaces between words — CJK
// runs as overlapping bigrams. Stopwords are dropped from the word form only.
func memoryTerms(text string) []string {
	var (
		terms []string
		seen  = map[string]bool{}
		word  []rune
		cjk   []rune
	)
	add := func(term string) {
		if seen[term] || memoryStopwords[term] {
			return
		}
		seen[term] = true
		terms = append(terms, term)
	}
	flushWord := func() {
		if len(word) >= 2 {
			add(string(word))
		}
		word = word[:0]
	}
	flushCJK := func() {
		if len(cjk) == 1 {
			add(string(cjk))
		}
		for i := 0; i+1 < len(cjk); i++ {
			add(string(cjk[i : i+2]))
		}
		cjk = cjk[:0]
	}

	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.Is(unicode.Han, r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			flushCJK()
			word = append(word, r)
		default:
			flushWord()
			flushCJK()
		}
	}
	flushWord()
	flushCJK()
	return terms
}

// memoryStopwords are the words that appear in every prompt and therefore say
// nothing about which fact is relevant. Kept small on purpose: an aggressive
// list turns a real query into no query.
var memoryStopwords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "by": true, "do": true, "does": true, "for": true, "from": true,
	"how": true, "i": true, "if": true, "in": true, "is": true, "it": true,
	"its": true, "me": true, "my": true, "no": true, "not": true, "of": true,
	"on": true, "or": true, "should": true, "so": true, "that": true, "the": true,
	"their": true, "then": true, "there": true, "these": true, "this": true,
	"to": true, "was": true, "we": true, "what": true, "when": true, "where": true,
	"which": true, "who": true, "why": true, "will": true, "with": true,
	"you": true, "your": true,
}
