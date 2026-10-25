package tui

import (
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/agent"
)

// memoryTUI is a TUI with a real local store in a temporary directory, which is
// what the commands talk to. The store is the same type the binary builds, so a
// command test cannot pass against a fake that accepts what the real one would
// refuse.
func memoryTUI(t *testing.T) (*TuiModel, *agent.LocalMemory) {
	t.Helper()
	store, err := agent.NewLocalMemory(t.TempDir(), "test", 0, 0)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	m := newTestTUI()
	m.agent.Memory = store
	m.agent.MemoryMode = agent.MemoryModeAuto
	return m, store
}

// TestRememberStoresAFact pins the only writer v1 has: the text lands in the
// store with the label the user wrote, and the reply says so.
func TestRememberStoresAFact(t *testing.T) {
	m, store := memoryTUI(t)

	m.handleCommand("/remember 测试命令: go test ./... -count=1")

	facts, err := store.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(facts) != 1 || facts[0].Key != "测试命令" || facts[0].Value != "go test ./... -count=1" {
		t.Fatalf("stored facts = %+v", facts)
	}
	if !strings.Contains(lastMessage(m), "remembered") {
		t.Fatalf("reply = %q", lastMessage(m))
	}
}

// TestRememberWithoutALabel pins the fallback label: one fact, not an error, and
// the label is the store's word rather than an invented topic.
func TestRememberWithoutALabel(t *testing.T) {
	m, store := memoryTUI(t)

	m.handleCommand("/remember the release tag is v0.0.7")

	facts, _ := store.List()
	if len(facts) != 1 {
		t.Fatalf("stored facts = %+v", facts)
	}
	if facts[0].Key != memoryFactKey || facts[0].Value != "the release tag is v0.0.7" {
		t.Fatalf("stored fact = %+v", facts[0])
	}
}

// TestRememberNeedsAnArgument pins the usage line rather than a silent no-op.
func TestRememberNeedsAnArgument(t *testing.T) {
	m, store := memoryTUI(t)
	m.handleCommand("/remember")
	if facts, _ := store.List(); len(facts) != 0 {
		t.Fatalf("an empty /remember stored something: %+v", facts)
	}
	if !strings.Contains(lastMessage(m), "usage: /remember") {
		t.Fatalf("reply = %q", lastMessage(m))
	}
}

// TestMemoryListsNewestFirst pins the listing: ids (what /memory forget takes),
// labels, and the newest facts first.
func TestMemoryListsNewestFirst(t *testing.T) {
	m, store := memoryTUI(t)
	for _, fact := range []struct{ key, value string }{
		{"first", "oldest fact"},
		{"second", "middle fact"},
		{"third", "newest fact"},
	} {
		if err := store.Remember(fact.key, fact.value); err != nil {
			t.Fatalf("remember: %v", err)
		}
	}

	m.handleCommand("/memory")

	reply := lastMessage(m)
	if !strings.Contains(reply, "3 facts") {
		t.Fatalf("reply = %q", reply)
	}
	newest := strings.Index(reply, "third")
	oldest := strings.Index(reply, "first")
	if newest < 0 || oldest < 0 || newest > oldest {
		t.Fatalf("the listing is not newest-first: %q", reply)
	}
	facts, _ := store.List()
	if !strings.Contains(reply, "["+facts[0].ID+"]") {
		t.Fatalf("the listing does not show the id /memory forget takes: %q", reply)
	}
}

// TestMemoryEmptyPin the empty case: a store with nothing in it says so instead
// of printing an empty listing.
func TestMemoryEmpty(t *testing.T) {
	m, _ := memoryTUI(t)
	m.handleCommand("/memory")
	if !strings.Contains(lastMessage(m), "no memories stored") {
		t.Fatalf("reply = %q", lastMessage(m))
	}
}

// TestMemoryForgetDeletesOneFact pins deletion by the id the listing shows, and
// that the store — not just the view — lost it.
func TestMemoryForgetDeletesOneFact(t *testing.T) {
	m, store := memoryTUI(t)
	if err := store.Remember("keep", "this one stays"); err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("drop", "this one goes"); err != nil {
		t.Fatal(err)
	}
	facts, _ := store.List()
	var dropID string
	for _, fact := range facts {
		if fact.Key == "drop" {
			dropID = fact.ID
		}
	}

	m.handleCommand("/memory forget " + dropID)
	if !strings.Contains(lastMessage(m), "forgot "+dropID) {
		t.Fatalf("reply = %q", lastMessage(m))
	}
	rest, _ := store.List()
	if len(rest) != 1 || rest[0].Key != "keep" {
		t.Fatalf("after forget: %+v", rest)
	}

	m.handleCommand("/memory forget " + dropID)
	if !strings.Contains(lastMessage(m), "no fact with id") {
		t.Fatalf("second forget reply = %q", lastMessage(m))
	}
}

// TestMemoryCommandsWithMemoryOff pins the message a user gets when the config
// switched memory off, rather than a command that silently does nothing.
func TestMemoryCommandsWithMemoryOff(t *testing.T) {
	m := newTestTUI()
	m.handleCommand("/remember a fact")
	if !strings.Contains(lastMessage(m), "memory is off") {
		t.Fatalf("/remember reply = %q", lastMessage(m))
	}
	m.handleCommand("/memory")
	if !strings.Contains(lastMessage(m), "memory is off") {
		t.Fatalf("/memory reply = %q", lastMessage(m))
	}
}

// TestSplitMemoryFact pins the optional label, including the two cases that must
// not split: a full-width colon splits, a clock time does not.
func TestSplitMemoryFact(t *testing.T) {
	tests := []struct {
		arg, key, value string
	}{
		{"label: value", "label", "value"},
		{"label：值", "label", "值"},
		{"no label here", memoryFactKey, "no label here"},
		{"12:30 standup", memoryFactKey, "12:30 standup"},
		{"https://example.com", memoryFactKey, "https://example.com"},
		{"trailing: ", memoryFactKey, "trailing:"},
	}
	for _, tc := range tests {
		key, value := splitMemoryFact(tc.arg)
		if key != tc.key || value != tc.value {
			t.Errorf("splitMemoryFact(%q) = (%q, %q), want (%q, %q)", tc.arg, key, value, tc.key, tc.value)
		}
	}
}

// TestCommandPaletteListsMemory pins the palette, which is where a user finds a
// command they have not memorised.
func TestCommandPaletteListsMemory(t *testing.T) {
	want := map[string]bool{"/remember": false, "/memory": false}
	for _, entry := range commandList() {
		if _, ok := want[entry.Name]; ok {
			want[entry.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("%s is missing from the command palette", name)
		}
	}
}
