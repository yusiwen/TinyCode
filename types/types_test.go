package types

import (
	"testing"
)

func TestRoleConstants(t *testing.T) {
	if RoleSystem != "system" {
		t.Fatalf("expected RoleSystem to be 'system', got %q", RoleSystem)
	}
	if RoleUser != "user" {
		t.Fatalf("expected RoleUser to be 'user', got %q", RoleUser)
	}
	if RoleAssistant != "assistant" {
		t.Fatalf("expected RoleAssistant to be 'assistant', got %q", RoleAssistant)
	}
	if RoleTool != "tool" {
		t.Fatalf("expected RoleTool to be 'tool', got %q", RoleTool)
	}
}

func TestMessageCreation(t *testing.T) {
	msg := Message{
		Role:    RoleUser,
		Content: "hello",
	}
	if msg.Role != RoleUser {
		t.Fatalf("expected Role %q, got %q", RoleUser, msg.Role)
	}
	if msg.Content != "hello" {
		t.Fatalf("expected Content 'hello', got %q", msg.Content)
	}
}

func TestMessageWithOptionalFields(t *testing.T) {
	toolCall := &ToolCall{
		ID:        "call_123",
		Name:      "read_file",
		Arguments: `{"path": "foo.txt"}`,
	}

	msg := Message{
		Role:       RoleAssistant,
		Content:    "",
		ToolCall:   toolCall,
		ToolCallID: "call_123",
	}

	if msg.ToolCall == nil {
		t.Fatal("expected ToolCall to be non-nil")
	}
	if msg.ToolCall.ID != "call_123" {
		t.Fatalf("expected ToolCall.ID 'call_123', got %q", msg.ToolCall.ID)
	}
	if msg.ToolCall.Name != "read_file" {
		t.Fatalf("expected ToolCall.Name 'read_file', got %q", msg.ToolCall.Name)
	}
}

func TestMemoryCreation(t *testing.T) {
	m := Memory{Key: "user_name", Value: "Alice"}
	if m.Key != "user_name" {
		t.Fatalf("expected Key 'user_name', got %q", m.Key)
	}
	if m.Value != "Alice" {
		t.Fatalf("expected Value 'Alice', got %q", m.Value)
	}
}

func TestChatRequestAndResponse(t *testing.T) {
	req := ChatRequest{
		Messages: []Message{
			{Role: RoleSystem, Content: "be helpful"},
			{Role: RoleUser, Content: "hi"},
		},
		MaxTokens: 100,
	}
	if len(req.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(req.Messages))
	}
	if req.MaxTokens != 100 {
		t.Fatalf("expected MaxTokens 100, got %d", req.MaxTokens)
	}

	resp := ChatResponse{
		Content: "Hello! How can I help?",
	}
	if resp.Content != "Hello! How can I help?" {
		t.Fatalf("expected Content 'Hello! How can I help?', got %q", resp.Content)
	}
}
