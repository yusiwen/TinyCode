package tui

import (
	"context"
	"testing"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/session"
	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/types"
)

// seedTranscript writes a session holding every shape a real transcript can
// have and returns its id. It is flushed to disk, because the point of these
// tests is the --resume path: a process that only ever had this file.
func seedTranscript(t *testing.T, dir, id string) {
	t.Helper()
	s := session.New(id, dir)
	for _, msg := range []types.Message{
		{Role: types.RoleUser, Content: "first question"},
		{Role: types.RoleAssistant, Content: "first answer", ReasoningContent: "private thinking"},
		{Role: types.RoleSystem, Content: "Switched theme: nord"},
		{Role: types.RoleAssistant, Content: "   "},
		{Role: types.RoleUser, Content: "second question"},
		{Role: types.RoleAssistant, Content: "second answer"},
	} {
		if err := s.Append(msg); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
}

// TestResumeRestoresModelHistory is the defect from issue #258: the transcript
// was restored to the screen and never to Agent.History, so the model answered
// the next prompt with no memory of a conversation the user could read.
func TestResumeRestoresModelHistory(t *testing.T) {
	dir := t.TempDir()
	seedTranscript(t, dir, "TUI-test-resume")

	m := newResumeTestTUI(dir, "TUI-test-resume", false)

	want := []types.Message{
		{Role: types.RoleUser, Content: "first question"},
		{Role: types.RoleAssistant, Content: "first answer"},
		{Role: types.RoleUser, Content: "second question"},
		{Role: types.RoleAssistant, Content: "second answer"},
	}
	got := m.agent.History
	if len(got) != len(want) {
		t.Fatalf("restored history has %d messages, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Errorf("history[%d] = %s %q, want %s %q", i, got[i].Role, got[i].Content, want[i].Role, want[i].Content)
		}
	}

	// A "system" note is interface text (command output), so it must not become
	// a turn the model reads; a blank assistant turn is skipped for the same
	// reason the loop skips it.
	for _, msg := range got {
		if msg.Role == types.RoleSystem {
			t.Errorf("command output leaked into the model history: %q", msg.Content)
		}
	}

	// The provider resends reasoning_content verbatim, so restoring a previous
	// turn's thinking would send it back as input.
	for i, msg := range got {
		if msg.ReasoningContent != "" {
			t.Errorf("history[%d] restored reasoning %q, want none", i, msg.ReasoningContent)
		}
	}

	// The screen keeps what the model drops: the command output is still there.
	found := false
	for _, cm := range m.messages {
		if cm.Role == "system" && cm.Content == "Switched theme: nord" {
			found = true
		}
	}
	if !found {
		t.Error("the resumed transcript no longer shows its command output")
	}
}

// TestResumedHistoryReachesTheFirstRequest closes the loop the issue describes:
// after a resume, the first request carries the restored turns in order, before
// the new prompt.
func TestResumedHistoryReachesTheFirstRequest(t *testing.T) {
	dir := t.TempDir()
	seedTranscript(t, dir, "TUI-test-request")

	var seen []types.Message
	provider := &agent.MockProvider{ChatFunc: func(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
		seen = append(seen, req.Messages...)
		return &types.ChatResponse{Content: "third answer"}, nil
	}}
	ag := agent.New(provider)
	m := NewTUI(ag, &config.Config{SessionDir: dir}, agent.NewRegistry(),
		agent.NewProviderRegistry([]agent.ProviderRecord{{Name: "test", Provider: provider}}),
		tool.NewTodoStore(), "TUI-test-request")

	if _, err := m.agent.Run(context.Background(), "third question"); err != nil {
		t.Fatalf("run: %v", err)
	}

	// The request is the system prompt, then the restored turns, then the new
	// prompt. Positions are compared, not just presence: order is what makes the
	// conversation readable to the model.
	order := []string{"first question", "first answer", "second question", "second answer", "third question"}
	last := -1
	for _, want := range order {
		at := -1
		for i, msg := range seen {
			if msg.Content == want && i > last {
				at = i
				break
			}
		}
		if at < 0 {
			t.Fatalf("%q is missing from the request, or out of order: %+v", want, seen)
		}
		last = at
	}
}
