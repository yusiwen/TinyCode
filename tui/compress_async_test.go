package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/types"
)

// stalledSummaryProvider answers agent steps with a canned reply and blocks the
// summarizer call until the caller's context is cancelled or the test releases
// it. It is how a slow or wedged endpoint looks from the TUI: the request never
// returns, so only a cancel can end it.
func stalledSummaryProvider(release <-chan struct{}, summaryCalls *int32) *agent.MockProvider {
	return &agent.MockProvider{ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
		if !isSummaryRequest(req) {
			return &types.ChatResponse{Content: "ok"}, nil
		}
		atomic.AddInt32(summaryCalls, 1)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return &types.ChatResponse{Content: "summary"}, nil
		}
	}}
}

// compressibleModel is a ready TUI whose history is long enough for /compress to
// summarize, wired to a stalled summarizer.
func compressibleModel(release <-chan struct{}, summaryCalls *int32) *TuiModel {
	m := newRunTestTUI(stalledSummaryProvider(release, summaryCalls))
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.agent.CompressionThreshold = 10
	m.agent.ContextLength = 1_000_000
	m.agent.History = compressibleHistory()
	return m
}

// updateOnGoroutine runs one Update the way Bubble Tea does — on its own
// goroutine — and delivers the command it returned. An Update that blocks here
// is blocking the event loop: no key, no frame and no cancel can get through.
func updateOnGoroutine(m *TuiModel, msg tea.Msg) <-chan tea.Cmd {
	ch := make(chan tea.Cmd, 1)
	go func() {
		_, cmd := m.Update(msg)
		ch <- cmd
	}()
	return ch
}

// awaitCommand waits for an Update to return, failing with why when the event
// loop never came back.
func awaitCommand(t *testing.T, ch <-chan tea.Cmd, why string) tea.Cmd {
	t.Helper()
	select {
	case cmd := <-ch:
		return cmd
	case <-time.After(3 * time.Second):
		t.Fatal(why)
		return nil
	}
}

// awaitCondition polls until cond holds, failing with why after the timeout.
func awaitCondition(t *testing.T, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(why)
}

// TestCompressKeepsTheEventLoopLive is the regression guard for the freeze in
// issue #3: /compress must hand the summarizer call to a command instead of
// running it on the Update goroutine, so the frame keeps rendering and Ctrl+C
// reaches the stalled request.
func TestCompressKeepsTheEventLoopLive(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	var summaryCalls int32
	m := compressibleModel(release, &summaryCalls)
	before := len(m.agent.History)

	m.input.SetValue("/compress")
	cmd := awaitCommand(t, updateOnGoroutine(m, tea.KeyMsg{Type: tea.KeyEnter}),
		"the event loop did not return from /compress: the summarizer still runs on the Update goroutine")
	if cmd == nil {
		t.Fatal("/compress returned no command, so nothing would run the summarizer")
	}

	// The summarizer is still stalled, yet the event loop stays live: a
	// keystroke is processed and the frame shows the in-flight status.
	awaitCommand(t, updateOnGoroutine(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")}),
		"typing blocked while the summarizer was stalled")
	if got := m.input.Value(); !strings.Contains(got, "x") {
		t.Errorf("keystroke was not processed during compression, input = %q", got)
	}
	if frame := stripANSIView(m.View()); !strings.Contains(frame, "Compressing") {
		t.Error("the frame does not show that a compression is in flight")
	}

	// Run the command the way Bubble Tea's event loop does.
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	awaitCondition(t, "the summarizer was never called", func() bool {
		return atomic.LoadInt32(&summaryCalls) == 1
	})

	// Ctrl+C must reach the stalled request.
	awaitCommand(t, updateOnGoroutine(m, tea.KeyMsg{Type: tea.KeyCtrlC}),
		"Ctrl+C blocked while the summarizer was stalled")
	select {
	case msg := <-done:
		m.Update(msg)
	case <-time.After(3 * time.Second):
		t.Fatal("Ctrl+C did not reach the summarizer: the compression is still running")
	}

	if got := len(m.agent.History); got != before {
		t.Errorf("a cancelled compression changed the history: %d -> %d messages", before, got)
	}
	if !strings.Contains(strings.ToLower(m.statusMsg), "cancel") {
		t.Errorf("expected a cancellation status, got %q", m.statusMsg)
	}
}

// TestRunRefusedWhileCompressing guards the other half of the concurrency
// contract: compression replaces agent History, so no run may start while it is
// in flight.
func TestRunRefusedWhileCompressing(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	var summaryCalls int32
	m := compressibleModel(release, &summaryCalls)
	before := len(m.agent.History)

	m.input.SetValue("/compress")
	cmd := awaitCommand(t, updateOnGoroutine(m, tea.KeyMsg{Type: tea.KeyEnter}),
		"the event loop did not return from /compress")
	if cmd == nil {
		t.Fatal("/compress returned no command, so nothing would run the summarizer")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	awaitCondition(t, "the summarizer was never called", func() bool {
		return atomic.LoadInt32(&summaryCalls) == 1
	})

	// A message submitted mid-compression must be refused, not run: the agent
	// loop would read History while the summary replaces it. The Enter path is
	// the first gate, and it must keep the typed text instead of losing it to a
	// refusal.
	m.input.SetValue("hello")
	runCmd := awaitCommand(t, updateOnGoroutine(m, tea.KeyMsg{Type: tea.KeyEnter}),
		"the event loop did not return while a run was requested during compression")
	if runCmd != nil {
		t.Error("a run was started while a compression owned the history")
	}
	if got := m.input.Value(); got != "hello" {
		t.Errorf("Enter during a compression discarded the typed text, input = %q", got)
	}
	if got := atomic.LoadInt32(&summaryCalls); got != 1 {
		t.Errorf("the provider saw %d calls, want only the summarizer call", got)
	}
	if !strings.Contains(strings.ToLower(m.statusMsg), "compression") {
		t.Errorf("expected a refusal that names the compression, got %q", m.statusMsg)
	}

	// The run state machine is the authority, not the key handler: a ChatMsg
	// that reaches Update from anywhere else must be refused there too.
	runCmd = awaitCommand(t, updateOnGoroutine(m, ChatMsg{Text: "hello"}),
		"the event loop did not return for a ChatMsg sent during compression")
	if runCmd != nil {
		t.Error("beginRun accepted a run while a compression owned the history")
	}
	if got := atomic.LoadInt32(&summaryCalls); got != 1 {
		t.Errorf("the provider saw %d calls after a raw ChatMsg, want only the summarizer call", got)
	}
	if m.runIsActive() {
		t.Error("a run became active although beginRun refused it")
	}
	if !m.compressIsActive() {
		t.Error("compression state was cleared while the summarizer was still stalled")
	}

	// Cancel and drain so no goroutine outlives the test.
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	select {
	case msg := <-done:
		m.Update(msg)
	case <-time.After(3 * time.Second):
		t.Fatal("Ctrl+C did not reach the summarizer")
	}
	if got := len(m.agent.History); got != before {
		t.Errorf("refused run still changed the history: %d -> %d messages", before, got)
	}
}
