package agent

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/types"
)

// newTestStore opens a store under a temporary directory with the default
// bounds, which is what every test that is not about the bounds wants.
func newTestStore(t *testing.T, dir, scope string) *LocalMemory {
	t.Helper()
	m, err := NewLocalMemory(dir, scope, 0, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return m
}

// TestLocalMemorySurvivesReopen pins the only property that makes the store a
// memory rather than a cache: what was written is still there in a new process.
func TestLocalMemorySurvivesReopen(t *testing.T) {
	dir := t.TempDir()

	first := newTestStore(t, dir, "proj")
	if err := first.Remember("test command", "go test ./... -count=1"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if err := first.Remember("editor", "neovim with gopls"); err != nil {
		t.Fatalf("remember: %v", err)
	}

	second := newTestStore(t, dir, "proj")
	list, err := second.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("reopened store has %d facts, want 2: %+v", len(list), list)
	}
	if list[0].Key != "test command" || list[1].Key != "editor" {
		t.Fatalf("facts came back in the wrong order: %+v", list)
	}
	if list[0].Source != "user" || list[0].ID == "" || list[0].CreatedAt.IsZero() {
		t.Fatalf("a fact lost its origin, id or timestamp: %+v", list[0])
	}
}

// TestLocalMemoryRememberIsIdempotent pins the id-as-content rule: the same fact
// typed twice is one fact, not two.
func TestLocalMemoryRememberIsIdempotent(t *testing.T) {
	m := newTestStore(t, t.TempDir(), "proj")
	for i := 0; i < 3; i++ {
		if err := m.Remember("editor", "neovim"); err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
	}
	list, err := m.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("remembering the same fact three times produced %d facts", len(list))
	}
	// A different label with the same text is a different fact: the key says
	// what the fact is about.
	if err := m.Remember("which editor", "neovim"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if list, _ = m.List(); len(list) != 2 {
		t.Fatalf("a differently labelled fact was folded into the first: %+v", list)
	}
}

// TestLocalMemoryDropsOldestPastMaxEntries pins the entry bound and that it is
// the oldest that goes.
func TestLocalMemoryDropsOldestPastMaxEntries(t *testing.T) {
	dir := t.TempDir()
	m, err := NewLocalMemory(dir, "proj", 3, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for i := 0; i < 5; i++ {
		if err := m.Remember(fmt.Sprintf("fact %d", i), fmt.Sprintf("value %d", i)); err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
	}
	list, err := m.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("store holds %d facts past a bound of 3: %+v", len(list), list)
	}
	for _, want := range []string{"fact 2", "fact 3", "fact 4"} {
		if !containsKey(list, want) {
			t.Fatalf("newest fact %q was dropped: %+v", want, list)
		}
	}
	if containsKey(list, "fact 0") {
		t.Fatalf("the oldest fact survived the bound: %+v", list)
	}
	// The file, not just the slice, is under the bound: a reopen agrees.
	reopened, err := NewLocalMemory(dir, "proj", 3, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if list, err = reopened.List(); err != nil || len(list) != 3 {
		t.Fatalf("reopened store = %d facts, %v", len(list), err)
	}
}

// TestLocalMemoryBoundsBytes pins the byte bound, which is the one a long value
// can cross without the entry count moving.
func TestLocalMemoryBoundsBytes(t *testing.T) {
	m, err := NewLocalMemory(t.TempDir(), "proj", 0, 400)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	for i := 0; i < 10; i++ {
		if err := m.Remember("long", strings.Repeat("x", 100)+fmt.Sprint(i)); err != nil {
			t.Fatalf("remember %d: %v", i, err)
		}
	}
	if got := m.size; got > 400 {
		t.Fatalf("store is %d bytes past a bound of 400", got)
	}
	list, _ := m.List()
	if len(list) == 0 {
		t.Fatal("the byte bound dropped every fact")
	}
	if !containsKey(list, "long") {
		t.Fatalf("list = %+v", list)
	}
}

// TestLocalMemoryRefusesAFactPastTheByteBound pins that a fact too large for the
// store is an error rather than an eviction: accepting it would empty the store
// of everything else to make room for one entry that still does not fit.
func TestLocalMemoryRefusesAFactPastTheByteBound(t *testing.T) {
	m, err := NewLocalMemory(t.TempDir(), "proj", 0, 200)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := m.Remember("small", "fits"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if err := m.Remember("huge", strings.Repeat("x", 500)); err == nil {
		t.Fatal("a fact past the byte bound was accepted")
	}
	list, err := m.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Key != "small" {
		t.Fatalf("the refused fact cost the store its contents: %+v", list)
	}
}

// TestLocalMemoryForgetDeletesAndPersists pins the delete the remote adapter
// cannot do: it removes exactly one fact, and a reopened store agrees.
func TestLocalMemoryForgetDeletesAndPersists(t *testing.T) {
	dir := t.TempDir()
	m := newTestStore(t, dir, "proj")
	if err := m.Remember("keep", "this one stays"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if err := m.Remember("drop", "this one goes"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	list, _ := m.List()
	var dropID string
	for _, fact := range list {
		if fact.Key == "drop" {
			dropID = fact.ID
		}
	}
	if dropID == "" {
		t.Fatal("the fact to delete has no id")
	}

	if err := m.Forget(dropID); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if list, _ = m.List(); len(list) != 1 || list[0].Key != "keep" {
		t.Fatalf("after forget: %+v", list)
	}
	if err := m.Forget(dropID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("second forget = %v, want ErrMemoryNotFound", err)
	}

	reopened := newTestStore(t, dir, "proj")
	if list, _ = reopened.List(); len(list) != 1 || list[0].Key != "keep" {
		t.Fatalf("the deleted fact came back after a reopen: %+v", list)
	}
}

// TestLocalMemoryRecallIsLexical pins what recall does and does not claim: a
// keyword hit ranks, a stopword-only query returns nothing, and the limit holds.
func TestLocalMemoryRecallIsLexical(t *testing.T) {
	m := newTestStore(t, t.TempDir(), "proj")
	facts := map[string]string{
		"test command":  "run go test ./... with HOME redirected under the sandbox",
		"editor":        "neovim, with gopls for Go",
		"release steps": "tag the commit, then push the tag",
	}
	for key, value := range facts {
		if err := m.Remember(key, value); err != nil {
			t.Fatalf("remember: %v", err)
		}
	}

	hits, err := m.Recall("how do I run the go tests", 5)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("a keyword query matched nothing")
	}
	if hits[0].Key != "test command" {
		t.Fatalf("the best hit for a test question is %q: %+v", hits[0].Key, hits)
	}
	for _, fact := range hits {
		if fact.Key == "release steps" {
			t.Fatalf("a fact with no shared term was returned: %+v", fact)
		}
	}

	if hits, err = m.Recall("the of and to", 5); err != nil || len(hits) != 0 {
		t.Fatalf("a query of stopwords returned %d facts, %v", len(hits), err)
	}
	if hits, err = m.Recall("go", 1); err != nil || len(hits) > 1 {
		t.Fatalf("limit 1 returned %d facts, %v", len(hits), err)
	}
}

// TestLocalMemoryRecallHandlesCJK pins that a prompt written without spaces is
// still searchable: the tokeniser uses bigrams for Han runs.
func TestLocalMemoryRecallHandlesCJK(t *testing.T) {
	m := newTestStore(t, t.TempDir(), "proj")
	if err := m.Remember("测试命令", "用 go test ./... 跑测试，HOME 要重定向"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if err := m.Remember("编辑器", "neovim 加 gopls"); err != nil {
		t.Fatalf("remember: %v", err)
	}

	hits, err := m.Recall("我要怎么跑测试", 5)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) == 0 || hits[0].Key != "测试命令" {
		t.Fatalf("a Chinese query did not find the Chinese fact: %+v", hits)
	}
}

// TestLocalMemoryRecallPrefersNewerOnATie pins the recency tiebreak, so a
// corrected fact ("use X, not Y") wins over the one it replaced.
func TestLocalMemoryRecallPrefersNewerOnATie(t *testing.T) {
	m := newTestStore(t, t.TempDir(), "proj")
	base := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return base }
	if err := m.Remember("deploy", "staging takes ten minutes"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	m.now = func() time.Time { return base.Add(time.Hour) }
	if err := m.Remember("deploy", "staging takes ten minutes"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	// Same text is one fact; a different value is the corrected one.
	m.now = func() time.Time { return base.Add(2 * time.Hour) }
	if err := m.Remember("deploy", "staging takes two minutes"); err != nil {
		t.Fatalf("remember: %v", err)
	}

	hits, err := m.Recall("staging deploy", 5)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("recall returned %d facts, want the two distinct ones: %+v", len(hits), hits)
	}
	if hits[0].Value != "staging takes two minutes" {
		t.Fatalf("the older fact won the tie: %+v", hits)
	}
}

// TestLocalMemoryConcurrentWriters pins that eight goroutines remembering at
// once all land: the mutex is the whole of the in-process guarantee.
func TestLocalMemoryConcurrentWriters(t *testing.T) {
	m := newTestStore(t, t.TempDir(), "proj")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := m.Remember(fmt.Sprintf("concurrent %d", i), fmt.Sprintf("value %d", i)); err != nil {
				t.Errorf("remember %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	list, err := m.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 8 {
		t.Fatalf("8 concurrent writes produced %d facts", len(list))
	}
}

// TestLocalMemoryShardsByScope pins that two projects sharing a directory do not
// read each other's facts.
func TestLocalMemoryShardsByScope(t *testing.T) {
	dir := t.TempDir()
	a := newTestStore(t, dir, MemoryScopeFor("/repo/a"))
	b := newTestStore(t, dir, MemoryScopeFor("/repo/b"))
	if err := a.Remember("only a", "visible in a"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if list, _ := b.List(); len(list) != 0 {
		t.Fatalf("project b sees project a's facts: %+v", list)
	}
	if got := MemoryScopeFor("/repo/a"); got != MemoryScopeFor("/repo/a/") {
		t.Fatalf("the scope is not stable across a trailing slash: %q", got)
	}
	if MemoryScopeFor("/repo/a") == MemoryScopeFor("/repo/b") {
		t.Fatal("two projects share one scope")
	}
	if got := MemoryScopeFor(""); got != "global" {
		t.Fatalf("an empty root resolved to %q", got)
	}
}

// TestLocalMemoryFileIsPrivateAndTolerant pins the two properties the file has
// to have: only the user can read it, and one unreadable line does not cost the
// rest of the store.
func TestLocalMemoryFileIsPrivateAndTolerant(t *testing.T) {
	dir := t.TempDir()
	m := newTestStore(t, dir, "proj")
	if err := m.Remember("keep", "readable fact"); err != nil {
		t.Fatalf("remember: %v", err)
	}
	info, err := os.Stat(m.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("store mode = %04o, want 0600", perm)
	}

	junk, err := os.OpenFile(m.Path(), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := junk.WriteString("not json\n"); err != nil {
		t.Fatalf("write junk: %v", err)
	}
	junk.Close()

	reopened := newTestStore(t, dir, "proj")
	list, err := reopened.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].Key != "keep" {
		t.Fatalf("an unreadable line cost the store its facts: %+v", list)
	}
}

func containsKey(list []types.Memory, key string) bool {
	for _, fact := range list {
		if fact.Key == key {
			return true
		}
	}
	return false
}

// TestMemoryBoundsMatchConfig pins the two layers' numbers. The store falls back
// to its own constants when the composition passes zero; the config layer carries
// the same three, and a drift between them would mean a configured bound that the
// store silently ignores.
func TestMemoryBoundsMatchConfig(t *testing.T) {
	if defaultLocalMemoryMaxEntries != config.DefaultMemoryMaxEntries {
		t.Fatalf("entries fallback = %d, config default = %d",
			defaultLocalMemoryMaxEntries, config.DefaultMemoryMaxEntries)
	}
	if defaultLocalMemoryMaxBytes != config.DefaultMemoryMaxBytes {
		t.Fatalf("bytes fallback = %d, config default = %d",
			defaultLocalMemoryMaxBytes, config.DefaultMemoryMaxBytes)
	}
	if defaultLocalMemoryRecall != config.DefaultMemoryRecallLimit {
		t.Fatalf("recall fallback = %d, config default = %d",
			defaultLocalMemoryRecall, config.DefaultMemoryRecallLimit)
	}
}
