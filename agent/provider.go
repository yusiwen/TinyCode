package agent

import (
	"context"

	"github.com/yusiwen/tinycode/types"
)

// LLMProvider is the abstraction over different LLM backends.
type LLMProvider interface {
	Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)
	Name() string
}

// Tool is a single action the agent can invoke.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
	Execute     func(ctx context.Context, args map[string]any) (string, error)
}
