package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/types"
)

// memoryFactKey is the label a fact typed without one gets. It says "a fact"
// rather than inventing a topic from the text: a label the store made up would
// read back in the next listing as if the user had written it.
const memoryFactKey = "fact"

// memoryListLimit is how many facts /memory prints before it summarises the
// rest. A store can hold thousands, and a listing that scrolls the conversation
// away is not a listing.
const memoryListLimit = 20

// memoryStoreForCommand returns the session's store and, when there is none, the
// sentence that says why. Memory is on by default, so the interesting case is a
// config that switched it off rather than a build that lacks it.
func (m *TuiModel) memoryStoreForCommand() (types.MemoryStore, string) {
	if m.agent == nil || m.agent.Memory == nil {
		return nil, "memory is off: set memory.enabled in your own ~/.tinycode/config.json (see docs/memory-decision.md)"
	}
	if m.agent.MemoryMode != agent.MemoryModeAuto {
		return nil, "memory is configured but not being recalled; nothing is stored or listed"
	}
	return m.agent.Memory, ""
}

// handleRemember stores one fact. It is the only writer the v1 store has: no
// model call, no summariser, nothing recorded unless a person asks for it
// (docs/memory-decision.md).
func (m *TuiModel) handleRemember(arg string) {
	store, why := m.memoryStoreForCommand()
	if store == nil {
		m.appendSystem(why)
		return
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		m.appendSystem("usage: /remember <fact>   or   /remember <label>: <fact>")
		return
	}
	key, value := splitMemoryFact(arg)
	if err := store.Remember(key, value); err != nil {
		m.appendSystem("remember failed: " + err.Error())
		return
	}
	m.appendSystem(fmt.Sprintf("remembered (%s): %s\n/memory lists it with the id that /memory forget takes.", key, value))
}

// handleMemory lists the store or forgets one fact. Deleting is part of the same
// surface on purpose: a store a person cannot read and correct is not one they
// can consent to.
func (m *TuiModel) handleMemory(arg string) {
	store, why := m.memoryStoreForCommand()
	if store == nil {
		m.appendSystem(why)
		return
	}
	fields := strings.Fields(arg)
	switch {
	case len(fields) == 0:
		facts, err := store.List()
		if err != nil {
			m.appendSystem("memory list failed: " + err.Error())
			return
		}
		if len(facts) == 0 {
			m.appendSystem("no memories stored; /remember <fact> adds one")
			return
		}
		shown := len(facts)
		if shown > memoryListLimit {
			shown = memoryListLimit
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d facts (newest %d):\n", len(facts), shown)
		// Newest first: the fact just written is the one a person is checking.
		for i := len(facts) - 1; i >= len(facts)-shown; i-- {
			fact := facts[i]
			fmt.Fprintf(&b, "  [%s] %s — %s\n", fact.ID, fact.Key, clipRunes(fact.Value, 100))
		}
		m.appendSystem(strings.TrimRight(b.String(), "\n"))
	case fields[0] == "forget" && len(fields) == 2:
		if err := store.Forget(fields[1]); err != nil {
			if errors.Is(err, agent.ErrMemoryNotFound) {
				m.appendSystem("no fact with id " + fields[1])
				return
			}
			m.appendSystem("forget failed: " + err.Error())
			return
		}
		m.appendSystem("forgot " + fields[1])
	default:
		m.appendSystem("usage: /memory   or   /memory forget <id>")
	}
}

// splitMemoryFact reads an optional label off the front of a remembered fact.
// Only a colon followed by a space (or the full-width colon) splits, so a time
// like 12:30 and a URL keep their colon and stay one fact.
func splitMemoryFact(arg string) (key, value string) {
	arg = strings.TrimSpace(arg)
	for _, sep := range []string{"：", ": "} {
		i := strings.Index(arg, sep)
		if i <= 0 {
			continue
		}
		key = strings.TrimSpace(arg[:i])
		value = strings.TrimSpace(arg[i+len(sep):])
		if key != "" && value != "" {
			return key, value
		}
	}
	return memoryFactKey, arg
}

// clipRunes shortens a value for the listing without cutting a character in
// half.
func clipRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}
