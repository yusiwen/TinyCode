package tui

import (
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// seedHistory gives the model n turns of two messages each, the shape a real
// transcript has.
func seedHistory(m *TuiModel, n int) {
	for i := 0; i < n; i++ {
		m.agent.History = append(m.agent.History,
			types.Message{Role: types.RoleUser, Content: "question"},
			types.Message{Role: types.RoleAssistant, Content: "answer"},
		)
	}
}

// TestPinCommand pins the reply and the effect: the newest turn, marked.
func TestPinCommand(t *testing.T) {
	m := newTestTUI()
	seedHistory(m, 2)

	m.handleCommand("/pin")

	if got := lastMessage(m); !strings.Contains(got, "pinned 1 turn(s); 1 pinned in total") {
		t.Fatalf("reply = %q", got)
	}
	if !m.agent.History[2].Pinned || !m.agent.History[3].Pinned {
		t.Fatal("the newest turn is not pinned")
	}
	if m.agent.History[0].Pinned {
		t.Fatal("an older turn was pinned too")
	}
}

// TestPinCommandWithACount reads the optional number, and rejects a bad one with
// the usage line rather than a silent no-op.
func TestPinCommandWithACount(t *testing.T) {
	m := newTestTUI()
	seedHistory(m, 3)

	m.handleCommand("/pin 2")
	if got := lastMessage(m); !strings.Contains(got, "pinned 2 turn(s); 2 pinned in total") {
		t.Fatalf("reply = %q", got)
	}

	m.handleCommand("/pin zero")
	if got := lastMessage(m); !strings.Contains(got, "usage: /pin [turns]") {
		t.Fatalf("reply = %q", got)
	}
}

// TestUnpinCommand clears the newest pin first.
func TestUnpinCommand(t *testing.T) {
	m := newTestTUI()
	seedHistory(m, 2)
	m.handleCommand("/pin 2")

	m.handleCommand("/unpin")
	if got := lastMessage(m); !strings.Contains(got, "unpinned 1 turn(s); 1 pinned in total") {
		t.Fatalf("reply = %q", got)
	}
	if m.agent.History[2].Pinned {
		t.Fatal("the newest turn is still pinned")
	}
	if !m.agent.History[0].Pinned {
		t.Fatal("the older pin went with it")
	}
}

// TestDropCommand removes the turn from the live context and says where the
// record still is.
func TestDropCommand(t *testing.T) {
	m := newTestTUI()
	seedHistory(m, 3)

	m.handleCommand("/drop 2")

	if got := len(m.agent.History); got != 2 {
		t.Fatalf("history has %d messages, want the first turn's 2", got)
	}
	reply := lastMessage(m)
	if !strings.Contains(reply, "dropped 2 turn(s)") || !strings.Contains(reply, "session file still holds them") {
		t.Fatalf("reply = %q", reply)
	}
}

// TestContextCommandsOnAnEmptyHistory pins the message a user gets before there
// is anything to act on.
func TestContextCommandsOnAnEmptyHistory(t *testing.T) {
	for _, tc := range []struct{ cmd, want string }{
		{"/pin", "nothing to pin"},
		{"/unpin", "nothing to unpin"},
		{"/drop", "nothing to drop"},
	} {
		m := newTestTUI()
		m.handleCommand(tc.cmd)
		if got := lastMessage(m); !strings.Contains(got, tc.want) {
			t.Errorf("%s reply = %q, want %q", tc.cmd, got, tc.want)
		}
	}
}

// TestContextCommandsRefusedDuringARun pins the guard: these commands replace
// Agent.History while the run goroutine appends to it.
func TestContextCommandsRefusedDuringARun(t *testing.T) {
	for _, cmd := range []string{"/pin", "/unpin", "/drop"} {
		m := newTestTUI()
		seedHistory(m, 2)
		m.runMu.Lock()
		m.runActive = true
		m.runMu.Unlock()

		m.handleCommand(cmd)

		if got := m.statusMsg; !strings.Contains(got, "a run is in progress") {
			t.Errorf("%s status = %q", cmd, got)
		}
		if len(m.agent.History) != 4 {
			t.Errorf("%s changed the history during a run", cmd)
		}
		m.runMu.Lock()
		m.runActive = false
		m.runMu.Unlock()
	}
}

// TestContextPaletteListsTheCommands pins discoverability.
func TestContextPaletteListsTheCommands(t *testing.T) {
	want := map[string]bool{"/pin": false, "/unpin": false, "/drop": false}
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
