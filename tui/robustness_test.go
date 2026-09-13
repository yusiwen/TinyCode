package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/types"
)

// gridModel builds a ready model with the given transcript for View() tests.
func gridModel(msgs []chatMessage) *TuiModel {
	m := layoutModel(40)
	m.charSelStart = selPos{Offset: -1}
	m.charSelEnd = selPos{Offset: -1}
	m.messages = msgs
	return m
}

// TestViewTodoDirtyWithoutMessagesNoPanic verifies that a dirty todo flag with
// an empty transcript neither panics nor leaves the flag set.
func TestViewTodoDirtyWithoutMessagesNoPanic(t *testing.T) {
	m := layoutModel(30)
	m.View() // initialize the grid so the next render takes the incremental path

	m.messages = nil
	m.msgDirty = nil
	m.msgRowCount = nil
	m.todoDirty = true
	ack := make(chan struct{}, 1)
	m.renderAckCh = ack

	out := m.View() // must not panic on messages[-1]

	if out == "" {
		t.Error("expected a non-empty view")
	}
	if m.todoDirty {
		t.Error("todoDirty must be cleared after View so the next render is consistent")
	}
	select {
	case <-ack:
	default:
		t.Error("todo render ack must fire even with no messages")
	}
}

// TestUnknownRoleKeepsRowAccountingConsistent verifies that a message whose
// role has no component contributes no grid rows (and no separator), and that
// an incremental re-render matches a full render.
func TestUnknownRoleKeepsRowAccountingConsistent(t *testing.T) {
	withUnknown := []chatMessage{
		{Role: "user", Content: "hello"},
		{Role: "bogus", Content: "must not render"},
		{Role: "assistant", Content: "world"},
	}
	known := []chatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "world"},
	}

	m := gridModel(withUnknown)
	m.View()

	if len(m.msgRowCount) != len(withUnknown) {
		t.Fatalf("expected one row count per message, got %d for %d messages",
			len(m.msgRowCount), len(withUnknown))
	}
	if m.msgRowCount[1] != 0 {
		t.Errorf("unknown role must record 0 rows, got %d", m.msgRowCount[1])
	}

	// The recorded rows plus one separator per pair of rendered messages must
	// equal the generated grid height.
	want := 0
	rendered := 0
	for i, rc := range m.msgRowCount {
		if !hasMsgComponent(withUnknown[i].Role) {
			continue
		}
		if rendered > 0 {
			want++ // blank separator before this rendered message
		}
		rendered++
		want += rc
	}
	if m.grid.RowCount() != want {
		t.Errorf("grid height = %d, msgRowCount/separator accounting = %d", m.grid.RowCount(), want)
	}

	ref := gridModel(known)
	ref.View()
	if m.grid.RowCount() != ref.grid.RowCount() {
		t.Errorf("unknown role changed the grid height: %d rows, want %d for the same visible messages",
			m.grid.RowCount(), ref.grid.RowCount())
	}

	// Re-rendering only the trailing assistant message must match a full render.
	m.msgDirty = []bool{false, false, true}
	m.View()
	if m.grid.RowCount() != ref.grid.RowCount() {
		t.Errorf("incremental re-render grid height = %d, want %d", m.grid.RowCount(), ref.grid.RowCount())
	}
	if len(m.lineSrcs) != len(ref.lineSrcs) {
		t.Errorf("incremental re-render lineSrcs = %d entries, want %d", len(m.lineSrcs), len(ref.lineSrcs))
	}
}

// TestSessionCountersTrackStreamAndToolCalls verifies that the status bar
// counters reflect streamed text/reasoning and reported tool calls.
func TestSessionCountersTrackStreamAndToolCalls(t *testing.T) {
	m := streamModel()
	m.messages = append(m.messages, chatMessage{Role: "assistant", Streaming: true})
	m.curAssistant = &m.messages[len(m.messages)-1]
	m.status = StatusStreaming

	// Character-by-character streaming must still accumulate: 8 bytes of text
	// (~2 tokens) plus 4 bytes of reasoning (~1 token).
	for i := 0; i < 8; i++ {
		m.Update(StreamMsg{TextDelta: "a"})
	}
	m.Update(StreamMsg{ReasoningDelta: "abcd"})
	m.Update(ToolCallMsg{Name: "bash", Arg: "ls"})
	m.Update(ToolCallMsg{Name: "read", Arg: "x"})

	if m.sessionTokens != 3 {
		t.Errorf("expected 3 tokens from streamed content, got %d", m.sessionTokens)
	}
	if m.sessionToolCalls != 2 {
		t.Errorf("expected 2 tool calls, got %d", m.sessionToolCalls)
	}

	bar := m.renderStatusBar()
	if !strings.Contains(bar, "tokens: 3") || !strings.Contains(bar, "tools: 2") {
		t.Errorf("status bar does not show the counters: %q", bar)
	}

	// Switching conversation clears the counters.
	m.resetSessionStats()
	if m.sessionTokens != 0 || m.sessionToolCalls != 0 {
		t.Errorf("expected counters to reset, got tokens=%d tools=%d", m.sessionTokens, m.sessionToolCalls)
	}
}

// compressibleHistory returns a transcript long enough for CompressHistory to
// summarize it (enough tokens and at least four user turns).
func compressibleHistory() []types.Message {
	return []types.Message{
		{Role: types.RoleUser, Content: strings.Repeat("u1 ", 40)},
		{Role: types.RoleAssistant, Content: strings.Repeat("a1 ", 40)},
		{Role: types.RoleUser, Content: strings.Repeat("u2 ", 40)},
		{Role: types.RoleAssistant, Content: strings.Repeat("a2 ", 40)},
		{Role: types.RoleUser, Content: strings.Repeat("u3 ", 40)},
		{Role: types.RoleAssistant, Content: strings.Repeat("a3 ", 40)},
		{Role: types.RoleUser, Content: strings.Repeat("u4 ", 40)},
		{Role: types.RoleAssistant, Content: strings.Repeat("a4 ", 40)},
		{Role: types.RoleUser, Content: strings.Repeat("u5 ", 40)},
		{Role: types.RoleAssistant, Content: strings.Repeat("a5 ", 40)},
		{Role: types.RoleUser, Content: strings.Repeat("u6 ", 40)},
		{Role: types.RoleAssistant, Content: strings.Repeat("a6 ", 40)},
	}
}

// isSummaryRequest reports whether a chat request is the compression
// summarizer call (as opposed to a normal agent step).
func isSummaryRequest(req types.ChatRequest) bool {
	for _, msg := range req.Messages {
		if strings.Contains(msg.Content, "Summarize the following conversation history") {
			return true
		}
	}
	return false
}

// TestCompressRefusedDuringActiveRun verifies that /compress never touches
// agent history while a run is in flight.
func TestCompressRefusedDuringActiveRun(t *testing.T) {
	started := make(chan struct{}, 1)
	var summaryCalls int32
	provider := &agent.MockProvider{ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
		if isSummaryRequest(req) {
			atomic.AddInt32(&summaryCalls, 1)
			return &types.ChatResponse{Content: "summary"}, nil
		}
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	m := newRunTestTUI(provider)

	if _, cmd := m.Update(ChatMsg{Text: "hello"}); cmd == nil {
		t.Fatal("expected a stream command after ChatMsg")
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("run never reached the provider")
	}
	if !m.runIsActive() {
		t.Fatal("precondition: a run should be active")
	}

	// Make a manual compression worthwhile, then attempt it mid-run. The
	// assignment is ordered after the agent goroutine's last History read
	// because it happens after the "started" handshake.
	m.agent.CompressionThreshold = 10
	m.agent.ContextLength = 1_000_000
	m.agent.History = compressibleHistory()
	before := len(m.agent.History)
	atomic.StoreInt32(&summaryCalls, 0)

	if _, cmd := m.handleCommand("/compress"); cmd != nil {
		t.Errorf("expected nil cmd from /compress, got %v", cmd)
	}
	if got := atomic.LoadInt32(&summaryCalls); got != 0 {
		t.Errorf("CompressHistory must not run during an active run (provider called %d times)", got)
	}
	if len(m.agent.History) != before {
		t.Errorf("history changed during an active run: %d -> %d messages", before, len(m.agent.History))
	}
	if !strings.Contains(m.statusMsg, "run is in progress") {
		t.Errorf("expected a clear refusal status, got %q", m.statusMsg)
	}

	// Clean up: cancel and let the run finish so no goroutine leaks.
	m.cancelRun()
	drainUntilTerminal(t, m, 3*time.Second)
}

// TestCompressWorksWhenIdle verifies that /compress still compresses history
// when no run is active.
func TestCompressWorksWhenIdle(t *testing.T) {
	var summaryCalls int32
	provider := &agent.MockProvider{ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
		if isSummaryRequest(req) {
			atomic.AddInt32(&summaryCalls, 1)
			return &types.ChatResponse{Content: "concise summary"}, nil
		}
		return &types.ChatResponse{Content: "unused"}, nil
	}}
	m := newRunTestTUI(provider)
	m.agent.CompressionThreshold = 10
	m.agent.ContextLength = 1_000_000
	m.agent.History = compressibleHistory()
	before := len(m.agent.History)

	if m.runIsActive() {
		t.Fatal("precondition: no run should be active")
	}
	if _, cmd := m.handleCommand("/compress"); cmd != nil {
		t.Errorf("expected nil cmd from /compress, got %v", cmd)
	}
	if got := atomic.LoadInt32(&summaryCalls); got != 1 {
		t.Errorf("expected CompressHistory to run once when idle, provider summary calls = %d", got)
	}
	if len(m.agent.History) >= before {
		t.Errorf("expected history to shrink, got %d -> %d messages", before, len(m.agent.History))
	}
	if !strings.Contains(m.statusMsg, "Compressed") {
		t.Errorf("expected a 'Compressed' status, got %q", m.statusMsg)
	}
}
