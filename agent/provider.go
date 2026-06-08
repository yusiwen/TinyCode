package agent

import (
	"context"
	"fmt"

	"github.com/yusiwen/tinycode/types"
)

// LLMProvider is the abstraction over different LLM backends.
type LLMProvider interface {
	Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)
	Name() string
}

// ProviderRecord holds one provider instance with its display name.
type ProviderRecord struct {
	Name     string
	Provider LLMProvider
}

// ProviderRegistry manages multiple LLM providers and tracks the active one.
type ProviderRegistry struct {
	records []ProviderRecord
	current int
}

// NewProviderRegistry creates a registry from provider instances.
func NewProviderRegistry(records []ProviderRecord) *ProviderRegistry {
	return &ProviderRegistry{
		records: records,
		current: 0,
	}
}

// Current returns the active LLM provider.
func (r *ProviderRegistry) Current() LLMProvider {
	if r == nil || len(r.records) == 0 {
		return nil
	}
	return r.records[r.current].Provider
}

// CurrentName returns the display name of the active provider.
func (r *ProviderRegistry) CurrentName() string {
	if r == nil || len(r.records) == 0 {
		return "none"
	}
	rec := r.records[r.current]
	return fmt.Sprintf("%s (%s)", rec.Name, rec.Provider.Name())
}

// List returns all provider entries for display.
func (r *ProviderRegistry) List() []ProviderRecord {
	if r == nil {
		return nil
	}
	return r.records
}

// Len returns the number of registered providers.
func (r *ProviderRegistry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.records)
}

// CurrentIndex returns the index of the active provider.
func (r *ProviderRegistry) CurrentIndex() int {
	if r == nil {
		return 0
	}
	return r.current
}

// SwitchTo switches to the provider at the given index.
func (r *ProviderRegistry) SwitchTo(idx int) error {
	if r == nil || idx < 0 || idx >= len(r.records) {
		return fmt.Errorf("invalid provider index %d", idx)
	}
	r.current = idx
	return nil
}

// SwitchToName switches to the provider with the given display name.
// Returns error if no matching provider is found.
func (r *ProviderRegistry) SwitchToName(name string) error {
	if r == nil {
		return fmt.Errorf("nil registry")
	}
	for i, rec := range r.records {
		if rec.Name == name {
			r.current = i
			return nil
		}
	}
	return fmt.Errorf("provider %q not found", name)
}

// Tool is a single action the agent can invoke.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Execute     func(ctx context.Context, args map[string]any) (string, error)
}

// MockProvider implements LLMProvider for testing (used by tui and other tests).
type MockProvider struct {
	ChatFunc func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)
	name     string
}

func (m *MockProvider) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	if m.ChatFunc != nil {
		return m.ChatFunc(ctx, req)
	}
	return &types.ChatResponse{Content: "mock response"}, nil
}

func (m *MockProvider) Name() string {
	if m.name != "" {
		return m.name
	}
	return "mock"
}
