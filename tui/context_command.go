package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// contextTurns reads the optional turn count: "/pin 2" acts on two turns, "/pin"
// on one.
func contextTurns(cmd, name string) (int, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, name))
	if rest == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("usage: %s [turns] — turns is a positive number", name)
	}
	return n, nil
}

// handleContextCommand implements /pin, /unpin and /drop.
//
// All three replace Agent.History while the run goroutine may be appending to
// it, so each is refused while a run is active — the same rule /compress
// follows, and for the same reason.
//
// None of them writes the session file: what is on disk is the record --resume
// reads, and a command meant to shape the live context should not quietly delete
// from that record.
func (m *TuiModel) handleContextCommand(cmd, name string) (tea.Model, tea.Cmd) {
	if m.agent == nil {
		m.ShowStatus("No agent available")
		return m, nil
	}
	if m.runIsActive() {
		m.ShowStatus("Cannot " + strings.TrimPrefix(name, "/") + ": a run is in progress")
		return m, nil
	}
	if name == "/keep" || name == "/unkeep" {
		m.handleKeep(cmd, name)
		return m, nil
	}

	n, err := contextTurns(cmd, name)
	if err != nil {
		m.appendSystem(err.Error())
		return m, nil
	}

	switch name {
	case "/pin":
		pinned, total := m.agent.PinLast(n)
		if pinned == 0 {
			m.appendSystem("nothing to pin: the history is empty")
			return m, nil
		}
		m.syncPinsToTranscript()
		m.persistSession()
		m.appendSystem(fmt.Sprintf(
			"pinned %d turn(s); %d pinned in total. A pinned turn is kept verbatim — neither /compress nor the automatic compression summarizes it.",
			pinned, total))
	case "/unpin":
		cleared, remaining := m.agent.UnpinLast(n)
		if cleared == 0 {
			m.appendSystem("nothing to unpin: no turn carries a pin")
			return m, nil
		}
		m.syncPinsToTranscript()
		m.persistSession()
		m.appendSystem(fmt.Sprintf("unpinned %d turn(s); %d pinned in total.", cleared, remaining))
	case "/drop":
		dropped := m.agent.DropLast(n)
		if dropped == 0 {
			m.appendSystem("nothing to drop: the history is empty")
			return m, nil
		}
		m.appendSystem(fmt.Sprintf(
			"dropped %d turn(s) from this context. The session file still holds them, so --resume brings them back.",
			dropped))
	}
	return m, nil
}

// syncPinsToTranscript mirrors the agent's pins into the visible transcript, so
// persistSession writes them with the messages they protect (S2 of #306). The
// transcript and the history grow together but are not index-aligned — system
// and banner messages exist only in the transcript — so the match is by role and
// content, newest first.
func (m *TuiModel) syncPinsToTranscript() {
	if m.agent == nil {
		return
	}
	for i := range m.messages {
		m.messages[i].Pinned = false
	}
	for _, turn := range m.agent.PinnedTurnTexts() {
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Role != "user" || m.messages[i].Content != turn[0] {
				continue
			}
			m.messages[i].Pinned = true
			if i+1 < len(m.messages) && m.messages[i+1].Role == "assistant" &&
				(turn[1] == "" || m.messages[i+1].Content == turn[1]) {
				m.messages[i+1].Pinned = true
			}
			break
		}
	}
}

// handleKeep manages the paths whose tool reads survive compaction (S4 of #306).
// The rule is set before a run — that is why it is a command and not something a
// mid-run model call decides — and it is persisted with the session.
func (m *TuiModel) handleKeep(cmd, name string) {
	arg := strings.TrimSpace(strings.TrimPrefix(cmd, name))
	if name == "/keep" && arg == "" {
		if len(m.agent.KeptPaths) == 0 {
			m.appendSystem("no kept paths; /keep <path> keeps a file's reads in context across a compaction")
			return
		}
		m.appendSystem("kept paths (their reads survive compaction):\n  " + strings.Join(m.agent.KeptPaths, "\n  "))
		return
	}
	if arg == "" {
		m.appendSystem("usage: /unkeep <path>")
		return
	}
	if name == "/keep" {
		if !m.agent.KeepPath(arg) {
			m.appendSystem("already kept: " + arg)
			return
		}
		m.persistSession()
		m.appendSystem("kept " + arg + ": a tool call that names it is kept verbatim when the run is compacted")
		return
	}
	if !m.agent.UnkeepPath(arg) {
		m.appendSystem("not kept: " + arg)
		return
	}
	m.persistSession()
	m.appendSystem("dropped " + arg + " from the kept paths")
}
