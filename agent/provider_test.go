package agent

import (
	"context"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// MockProvider implements LLMProvider for testing.
type MockProvider struct {
	chatFunc func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)
	name     string
}

func (m *MockProvider) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	if m.chatFunc != nil {
		return m.chatFunc(ctx, req)
	}
	return &types.ChatResponse{Content: "mock response"}, nil
}

func (m *MockProvider) Name() string {
	if m.name != "" {
		return m.name
	}
	return "mock"
}

// compile-time check that MockProvider satisfies LLMProvider.
var _ LLMProvider = (*MockProvider)(nil)

func TestMockProviderImplementsInterface(t *testing.T) {
	var p LLMProvider = &MockProvider{}
	if p.Name() != "mock" {
		t.Fatalf("expected Name 'mock', got %q", p.Name())
	}
}

func TestMockProviderChat(t *testing.T) {
	p := &MockProvider{}
	ctx := context.Background()
	resp, err := p.Chat(ctx, types.ChatRequest{
		Messages: []types.Message{
			{Role: types.RoleUser, Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "mock response" {
		t.Fatalf("expected Content 'mock response', got %q", resp.Content)
	}
}

func TestMockProviderCustomName(t *testing.T) {
	p := &MockProvider{name: "custom"}
	if p.Name() != "custom" {
		t.Fatalf("expected Name 'custom', got %q", p.Name())
	}
}

func TestMockProviderCustomChat(t *testing.T) {
	p := &MockProvider{
		chatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{Content: "custom reply"}, nil
		},
	}
	ctx := context.Background()
	resp, err := p.Chat(ctx, types.ChatRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "custom reply" {
		t.Fatalf("expected Content 'custom reply', got %q", resp.Content)
	}
}
