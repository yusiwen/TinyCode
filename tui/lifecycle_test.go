package tui

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/session"
	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/types"
)

// newRunTestTUI builds a TUI whose agent uses the given mock provider.
func newRunTestTUI(provider *agent.MockProvider) *TuiModel {
	return NewTUI(agent.New(provider), &config.Config{}, agent.NewRegistry(),
		agent.NewProviderRegistry([]agent.ProviderRecord{
			{Name: "test", Provider: provider},
		}), tool.NewTodoStore())
}

// newResumeTestTUI builds a TUI that resumes the given session id.
func newResumeTestTUI(dir, resumeID string, withTool bool) *TuiModel {
	ag := agent.New(nil)
	if withTool {
		ag.AddTool(agent.Tool{Name: "noop", Description: "noop"})
	}
	provider := &agent.MockProvider{}
	return NewTUI(ag, &config.Config{SessionDir: dir}, agent.NewRegistry(),
		agent.NewProviderRegistry([]agent.ProviderRecord{
			{Name: "test", Provider: provider},
		}), tool.NewTodoStore(), resumeID)
}

// drainUntilTerminal processes stream messages until the terminal StreamDone
// arrives, failing the test if that takes longer than the timeout.
func drainUntilTerminal(t *testing.T, m *TuiModel, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case msg := <-m.streamCh:
			m.Update(msg)
			if sd, ok := msg.(StreamDone); ok && !sd.IsIntermediate {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for terminal StreamDone")
		}
	}
}

// TestInterruptCancelsRunContext verifies that Ctrl+C cancels the running
// agent's context, that the status stays Streaming until the run reports
// completion, and that the run actually terminates.
func TestInterruptCancelsRunContext(t *testing.T) {
	started := make(chan struct{}, 1)
	var calls int32
	provider := &agent.MockProvider{ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
		atomic.AddInt32(&calls, 1)
		started <- struct{}{}
		<-ctx.Done() // block until the run context is cancelled
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
		t.Fatal("expected runActive=true while the run is in flight")
	}

	// Interrupt.
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil {
		t.Errorf("expected nil cmd on interrupt (copy/quit suppressed), got %v", cmd)
	}
	// Status must remain Streaming until the run reports completion.
	if m.status != StatusStreaming {
		t.Fatalf("expected StatusStreaming right after interrupt, got %v", m.status)
	}

	// The run must unwind and deliver a terminal StreamDone.
	drainUntilTerminal(t, m, 3*time.Second)

	if m.status != StatusIdle {
		t.Errorf("expected StatusIdle after the run completed, got %v", m.status)
	}
	if m.runIsActive() {
		t.Error("expected runActive=false after the run completed")
	}
	if m.curAssistant != nil {
		t.Error("expected curAssistant to be cleared after the terminal StreamDone")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected exactly one provider call, got %d", got)
	}
	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.Content, "Interrupted") {
		t.Errorf("expected interrupted marker in last message, got %q", last.Content)
	}
}

// TestSecondSubmitDoesNotStartConcurrentRun verifies that submitting again
// while a run is active neither starts a second run nor appends messages.
func TestSecondSubmitDoesNotStartConcurrentRun(t *testing.T) {
	started := make(chan struct{}, 1)
	var calls int32
	provider := &agent.MockProvider{ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
		atomic.AddInt32(&calls, 1)
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	m := newRunTestTUI(provider)

	m.Update(ChatMsg{Text: "first"})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("run never reached the provider")
	}
	msgCount := len(m.messages)

	// A raw second ChatMsg (e.g. a stale command) must be refused.
	if _, cmd := m.Update(ChatMsg{Text: "second"}); cmd != nil {
		t.Errorf("expected nil cmd for a refused second submit, got %v", cmd)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected one provider call, got %d (concurrent run started)", got)
	}
	if len(m.messages) != msgCount {
		t.Errorf("expected no new messages for a refused submit, got %d (was %d)", len(m.messages), msgCount)
	}

	// Enter is also refused while the run is active.
	m.input.SetValue("third")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected one provider call after Enter, got %d", got)
	}
	if len(m.messages) != msgCount {
		t.Errorf("expected no new messages after Enter, got %d", len(m.messages))
	}

	// Clean up: cancel and let the run finish so no goroutine leaks.
	m.cancelRun()
	drainUntilTerminal(t, m, 3*time.Second)
}

// TestStreamDoneNilCurAssistantNoPanic verifies the nil curAssistant paths.
func TestStreamDoneNilCurAssistantNoPanic(t *testing.T) {
	m := streamModel()
	m.status = StatusStreaming
	m.curAssistant = nil

	m.Update(ToolCallMsg{Name: "bash", Arg: "ls"})
	m.Update(ToolResultMsg{Name: "bash"})
	m.Update(StreamDone{Content: "late text"})
	if m.status != StatusIdle {
		t.Errorf("expected StatusIdle after terminal StreamDone, got %v", m.status)
	}

	m.Update(StreamDone{Error: errTestFailure})
}

// errTestFailure is a non-cancellation error used by the nil-guard test.
var errTestFailure = &testError{"boom"}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// TestSupersededRunMessagesIgnored verifies that output from a run that was
// replaced by a newer generation is dropped instead of corrupting state.
func TestSupersededRunMessagesIgnored(t *testing.T) {
	m := streamModel()
	m.messages = append(m.messages, chatMessage{Role: "assistant", Streaming: true})
	m.curAssistant = &m.messages[len(m.messages)-1]
	m.status = StatusStreaming
	// Simulate run 1 in flight, then run 2 taking over.
	m.runID = 1
	m.runActive = true
	m.runID = 2

	// Stale run 1 terminal message must not clear the new run's assistant.
	m.Update(StreamDone{RunID: 1, Content: "old"})
	if m.curAssistant == nil {
		t.Fatal("stale StreamDone cleared curAssistant")
	}
	if m.status != StatusStreaming {
		t.Errorf("stale StreamDone changed status to %v", m.status)
	}
	if m.curAssistant.Content != "" {
		t.Errorf("stale StreamDone wrote content %q", m.curAssistant.Content)
	}

	// Current run 2 terminal message is applied.
	m.Update(StreamDone{RunID: 2, Content: "new"})
	if m.curAssistant != nil {
		t.Error("current StreamDone should clear curAssistant")
	}
	if m.status != StatusIdle {
		t.Errorf("expected StatusIdle after current terminal StreamDone, got %v", m.status)
	}
	last := m.messages[len(m.messages)-1]
	if last.Streaming {
		t.Error("current StreamDone should clear Streaming")
	}
	if len(last.Blocks) == 0 {
		t.Error("expected current run content to be rendered into Blocks")
	}
}

// TestResumeSetsCurrentBranch verifies --resume records the active session.
func TestResumeSetsCurrentBranch(t *testing.T) {
	dir := t.TempDir()
	const id = "TUI-20260101-000000"
	s := session.New(id, dir)
	s.Append(types.Message{Role: "user", Content: "old message"})
	if err := s.Flush(); err != nil {
		t.Fatalf("flush session: %v", err)
	}

	m := newResumeTestTUI(dir, id, true)
	if m.currentBranch != id {
		t.Errorf("expected currentBranch=%q after resume, got %q", id, m.currentBranch)
	}
	foundBanner := false
	for _, cm := range m.messages {
		if strings.Contains(cm.Content, "Resumed session: "+id) {
			foundBanner = true
		}
	}
	if !foundBanner {
		t.Error("expected a 'Resumed session' confirmation on successful resume")
	}
}

// TestResumeFailureSurfacesError verifies a failed resume reports in the
// transcript and does not claim to have resumed.
func TestResumeFailureSurfacesError(t *testing.T) {
	dir := t.TempDir()
	m := newResumeTestTUI(dir, "does-not-exist", true)
	if m.currentBranch != "" {
		t.Errorf("expected empty currentBranch on failed resume, got %q", m.currentBranch)
	}
	sawError := false
	for _, cm := range m.messages {
		if strings.Contains(cm.Content, "Failed to resume session") {
			sawError = true
		}
		if strings.Contains(cm.Content, "Resumed session") {
			t.Errorf("must not report success on failed resume: %q", cm.Content)
		}
	}
	if !sawError {
		t.Error("expected the resume error to be surfaced in the transcript")
	}
}

// TestExitPersistsResumedSession verifies /exit rewrites the resumed session
// file instead of creating a duplicate TUI-<timestamp> session.
func TestExitPersistsResumedSession(t *testing.T) {
	dir := t.TempDir()
	const id = "TUI-20260101-000000"
	s := session.New(id, dir)
	s.Append(types.Message{Role: "user", Content: "old message"})
	if err := s.Flush(); err != nil {
		t.Fatalf("flush session: %v", err)
	}

	m := newResumeTestTUI(dir, id, false)
	m.messages = append(m.messages, chatMessage{Role: "user", Content: "new message"})

	_, cmd := m.handleCommand("/exit")
	if cmd == nil {
		t.Fatal("expected quit cmd for /exit")
	}

	loaded, err := session.Load(id, dir)
	if err != nil {
		t.Fatalf("load resumed session: %v", err)
	}
	sawNew := false
	for _, msg := range loaded.Messages {
		if msg.Content == "new message" {
			sawNew = true
		}
	}
	if !sawNew {
		t.Errorf("expected the resumed session file to be updated, messages: %+v", loaded.Messages)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read session dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != id+".json" {
			t.Errorf("unexpected extra session file %q (session was duplicated)", e.Name())
		}
	}
}

// TestCtrlCQuitPersistsResumedSession verifies the double-Ctrl+C quit path
// also updates the resumed session instead of duplicating it.
func TestCtrlCQuitPersistsResumedSession(t *testing.T) {
	dir := t.TempDir()
	const id = "TUI-20260101-111111"
	s := session.New(id, dir)
	s.Append(types.Message{Role: "user", Content: "old message"})
	if err := s.Flush(); err != nil {
		t.Fatalf("flush session: %v", err)
	}

	m := newResumeTestTUI(dir, id, false)
	m.messages = append(m.messages, chatMessage{Role: "user", Content: "second message"})

	// First tap arms the quit confirmation; second tap persists and quits.
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil {
		t.Fatalf("expected nil cmd on first Ctrl+C, got %v", cmd)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected quit cmd on second Ctrl+C")
	}

	loaded, err := session.Load(id, dir)
	if err != nil {
		t.Fatalf("load resumed session: %v", err)
	}
	sawNew := false
	for _, msg := range loaded.Messages {
		if msg.Content == "second message" {
			sawNew = true
		}
	}
	if !sawNew {
		t.Errorf("expected the resumed session file to be updated, messages: %+v", loaded.Messages)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read session dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != id+".json" {
			t.Errorf("unexpected extra session file %q (session was duplicated)", e.Name())
		}
	}
}

// TestSessionTitleGenerationIsAsync verifies that session-title generation is
// deferred to the returned command and never blocks Update.
func TestSessionTitleGenerationIsAsync(t *testing.T) {
	var calls int32
	provider := &agent.MockProvider{ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
		atomic.AddInt32(&calls, 1)
		return &types.ChatResponse{Content: "Async Title"}, nil
	}}
	m := newRunTestTUI(provider)
	m.messages = []chatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}

	cmd := m.generateSessionTitleCmd()
	if cmd == nil {
		t.Fatal("expected a title command")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("title LLM call must not run synchronously during Update, got %d calls", got)
	}

	msg := cmd()
	st, ok := msg.(sessionTitleMsg)
	if !ok {
		t.Fatalf("expected sessionTitleMsg, got %T", msg)
	}
	if st.Title != "Async Title" {
		t.Errorf("expected 'Async Title', got %q", st.Title)
	}

	m.Update(st)
	if m.sessionTitle != "Async Title" {
		t.Errorf("expected sessionTitle to be applied, got %q", m.sessionTitle)
	}
}

// TestSessionTitleCmdTimeout verifies the background title call is bounded.
func TestSessionTitleCmdTimeout(t *testing.T) {
	old := sessionTitleTimeout
	sessionTitleTimeout = 50 * time.Millisecond
	defer func() { sessionTitleTimeout = old }()

	provider := &agent.MockProvider{ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	m := newRunTestTUI(provider)
	m.messages = []chatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}

	cmd := m.generateSessionTitleCmd()
	if cmd == nil {
		t.Fatal("expected a title command")
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		st, ok := msg.(sessionTitleMsg)
		if !ok {
			t.Fatalf("expected sessionTitleMsg, got %T", msg)
		}
		if st.Title == "" {
			t.Error("expected a fallback title after the timeout")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("title command did not respect its timeout")
	}
}
