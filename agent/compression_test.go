package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"Hello", 1},        // 5 chars / 4 = 1
		{"Hello World!", 3}, // 12 chars / 4 = 3
		{"", 0},
	}
	for _, tt := range tests {
		got := EstimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestEstimateMessagesTokens(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi there!"},
	}
	tokens := EstimateMessagesTokens(msgs)
	if tokens <= 0 {
		t.Errorf("expected positive token count, got %d", tokens)
	}
}

func TestCompressHistoryTooSmall(t *testing.T) {
	a := &Agent{
		CompressionThreshold: 100,
		ContextLength:        1000,
		Provider:             &MockProvider{},
	}
	history := []types.Message{
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "Hello!"},
	}
	result, err := a.compressHistory(context.Background(), history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != len(history) {
		t.Errorf("expected %d messages (unchanged), got %d", len(history), len(result))
	}
}

func TestCompressHistoryDisabled(t *testing.T) {
	a := &Agent{
		CompressionThreshold: 0, // disabled
		ContextLength:        1000,
	}
	history := []types.Message{
		{Role: "user", Content: "Hi"},
	}
	result, err := a.compressHistory(context.Background(), history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != len(history) {
		t.Errorf("expected %d messages (unchanged), got %d", len(history), len(result))
	}
}

func TestCompressHistoryBelowThreshold(t *testing.T) {
	a := &Agent{
		CompressionThreshold: 99999, // very high — won't trigger
		ContextLength:        100000,
	}
	history := []types.Message{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "World"},
	}
	result, err := a.compressHistory(context.Background(), history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != len(history) {
		t.Errorf("expected %d messages, got %d", len(history), len(result))
	}
}

func TestTokenThresholdUnit(t *testing.T) {
	// Verify compression triggered at threshold with a mock provider that supports Chat
	a := &Agent{
		CompressionThreshold: 100,
		ContextLength:        200,
		Provider: &MockProvider{
			ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
				return &types.ChatResponse{Content: "Summary of the conversation."}, nil
			},
		},
	}
	var history []types.Message
	for i := 0; i < 10; i++ {
		history = append(history,
			types.Message{Role: types.RoleUser, Content: "This is a long user message that should accumulate tokens."},
			types.Message{Role: types.RoleAssistant, Content: "This is a long assistant response with detailed analysis."},
		)
	}
	tokens := EstimateMessagesTokens(history)
	if tokens < 100 {
		t.Errorf("expected tokens >= 100, got %d", tokens)
	}
	// Should trigger compression and succeed (mock returns a summary)
	result, err := a.compressHistory(context.Background(), history)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) >= len(history) {
		t.Errorf("expected fewer messages after compression, got %d (was %d)", len(result), len(history))
	}
	// Verify the summary message was inserted
	hasSummary := false
	for _, m := range result {
		if strings.Contains(m.Content, "COMPRESSED HISTORY") {
			hasSummary = true
			break
		}
	}
	if !hasSummary {
		t.Error("expected [COMPRESSED HISTORY] message in compressed output")
	}
}

// mockTodoStorer implements the TodoStorer interface for testing.
type mockTodoStorer struct {
	snapshot string
}

func (m *mockTodoStorer) FormatForInjection() string {
	return m.snapshot
}

func TestCompressTodoInjection(t *testing.T) {
	a := &Agent{
		CompressionThreshold: 50,
		ContextLength:        200,
		TodoStorer: &mockTodoStorer{
			snapshot: "[>] Fix bug\n[ ] Write test\n",
		},
		Provider: &MockProvider{
			ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
				return &types.ChatResponse{Content: "Summary."}, nil
			},
		},
	}
	var history []types.Message
	for i := 0; i < 8; i++ {
		history = append(history,
			types.Message{Role: types.RoleUser, Content: "A long enough user message that accumulates tokens."},
			types.Message{Role: types.RoleAssistant, Content: "A detailed assistant response with analysis and code."},
		)
	}
	result, err := a.compressHistory(context.Background(), history)
	if err != nil {
		t.Fatalf("compress error: %v", err)
	}
	// Should contain ACTIVE TODO ITEMS
	found := false
	for _, m := range result {
		if strings.Contains(m.Content, "ACTIVE TODO ITEMS") {
			found = true
			if !strings.Contains(m.Content, "Fix bug") || !strings.Contains(m.Content, "Write test") {
				t.Error("expected todo items in injection")
			}
			break
		}
	}
	if !found {
		t.Error("expected [ACTIVE TODO ITEMS] in compressed output")
	}
}

func TestCompressNoTodoInjection(t *testing.T) {
	a := &Agent{
		CompressionThreshold: 50,
		ContextLength:        200,
		TodoStorer:           &mockTodoStorer{snapshot: ""},
		Provider: &MockProvider{
			ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
				return &types.ChatResponse{Content: "Summary."}, nil
			},
		},
	}
	var history []types.Message
	for i := 0; i < 8; i++ {
		history = append(history,
			types.Message{Role: types.RoleUser, Content: "A long enough user message."},
			types.Message{Role: types.RoleAssistant, Content: "A detailed assistant response."},
		)
	}
	result, err := a.compressHistory(context.Background(), history)
	if err != nil {
		t.Fatalf("compress error: %v", err)
	}
	for _, m := range result {
		if strings.Contains(m.Content, "ACTIVE TODO ITEMS") {
			t.Error("expected no todo injection when storer is empty")
			break
		}
	}
}

// validateToolGroups asserts that every assistant message with tool_calls is
// followed by a tool result for each of its ids, and that no tool result
// appears without its assistant message. A history that violates this is
// rejected by OpenAI-compatible APIs.
func validateToolGroups(t *testing.T, msgs []types.Message) {
	t.Helper()
	pending := map[string]bool{}
	for i, m := range msgs {
		switch m.Role {
		case types.RoleAssistant:
			if len(pending) > 0 {
				t.Errorf("message %d: assistant message starts before all tool results arrived (%v)", i, pending)
			}
			for _, tc := range m.ToolCalls {
				pending[tc.ID] = true
			}
		case types.RoleTool:
			if !pending[m.ToolCallID] {
				t.Errorf("message %d: tool result %q has no preceding assistant tool_call", i, m.ToolCallID)
				continue
			}
			delete(pending, m.ToolCallID)
		}
	}
	if len(pending) > 0 {
		t.Errorf("assistant tool_calls without tool results: %v", pending)
	}
}

// TestCompressHistoryKeepsToolGroupsIntact guards the boundary bug: the old
// fixed offset could keep an assistant tool_calls message while summarizing its
// remaining tool results away.
func TestCompressHistoryKeepsToolGroupsIntact(t *testing.T) {
	a := &Agent{
		CompressionThreshold: 10,
		ContextLength:        1000,
		Provider:             &MockProvider{},
	}

	history := []types.Message{
		{Role: types.RoleUser, Content: "u1"},
		{Role: types.RoleAssistant, Content: "a1"},
		{Role: types.RoleUser, Content: "u2"},
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{
			{ID: "X", Name: "bash"}, {ID: "Y", Name: "bash"}, {ID: "Z", Name: "bash"},
		}},
		{Role: types.RoleTool, ToolCallID: "X", Name: "bash", Content: "x"},
		{Role: types.RoleTool, ToolCallID: "Y", Name: "bash", Content: "y"},
		{Role: types.RoleTool, ToolCallID: "Z", Name: "bash", Content: "z"},
		{Role: types.RoleAssistant, Content: "a2"},
		{Role: types.RoleUser, Content: "u3"},
		{Role: types.RoleAssistant, Content: "a3"},
		{Role: types.RoleUser, Content: "u4"},
		{Role: types.RoleAssistant, Content: "a4"},
		{Role: types.RoleUser, Content: "u5"},
		{Role: types.RoleAssistant, Content: "a5"},
	}

	got, err := a.compressHistory(context.Background(), history)
	if err != nil {
		t.Fatalf("compressHistory: %v", err)
	}
	if len(got) >= len(history) {
		t.Fatalf("expected compression to shrink the history, got %d of %d", len(got), len(history))
	}
	validateToolGroups(t, got)

	// The three tool results of the protected group must all be present.
	for _, id := range []string{"X", "Y", "Z"} {
		found := false
		for _, m := range got {
			if m.Role == types.RoleTool && m.ToolCallID == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("tool result %q was summarized away while its assistant message was kept", id)
		}
	}
}

// TestCompressHistoryKeepsPriorSummary checks that an earlier compressed
// summary lands in the summarization input instead of being dropped.
func TestCompressHistoryKeepsPriorSummary(t *testing.T) {
	var summarizerInput string
	a := &Agent{
		CompressionThreshold: 10,
		ContextLength:        1000,
		Provider: &MockProvider{ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			if len(req.Messages) > 1 {
				summarizerInput = req.Messages[1].Content
			}
			return &types.ChatResponse{Content: "NEW SUMMARY"}, nil
		}},
	}

	const oldSummary = "OLD-SUMMARY-MARKER"
	// The system message sits between u4 and a4 so that it falls inside the
	// summarized middle region.
	history := []types.Message{
		{Role: types.RoleUser, Content: "u1"},
		{Role: types.RoleAssistant, Content: "a1"},
		{Role: types.RoleUser, Content: "u2"},
		{Role: types.RoleAssistant, Content: "a2"},
		{Role: types.RoleUser, Content: "u3"},
		{Role: types.RoleAssistant, Content: "a3"},
		{Role: types.RoleUser, Content: "u4"},
		{Role: types.RoleSystem, Content: oldSummary},
		{Role: types.RoleAssistant, Content: "a4"},
		{Role: types.RoleUser, Content: "u5"},
		{Role: types.RoleAssistant, Content: "a5"},
		{Role: types.RoleUser, Content: "u6"},
		{Role: types.RoleAssistant, Content: "a6"},
	}

	if _, err := a.compressHistory(context.Background(), history); err != nil {
		t.Fatalf("compressHistory: %v", err)
	}
	if !strings.Contains(summarizerInput, oldSummary) {
		t.Errorf("prior summary missing from the summarization input:\n%s", summarizerInput)
	}
}
