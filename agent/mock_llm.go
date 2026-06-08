package agent

import (
	"context"
	"fmt"

	"github.com/yusiwen/tinycode/types"
)

// MockStep defines one LLM response in a multi-step sequence.
type MockStep struct {
	// What the LLM returns (either Content or ToolCalls, not both)
	Content    string
	ToolCalls  []types.ToolCall
	// Expected tool results fed back to the LLM in the NEXT call
	ToolResults []string
}

// MockLLM simulates a multi-step LLM for testing the agent loop.
type MockLLM struct {
	Steps     []MockStep
	callCount int
	name      string
}

func NewMockLLM(steps []MockStep) *MockLLM {
	return &MockLLM{Steps: steps, name: "mock-llm"}
}

func (m *MockLLM) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	if m.callCount >= len(m.Steps) {
		return &types.ChatResponse{Content: "mock response (exhausted)"}, nil
	}
	step := m.Steps[m.callCount]
	m.callCount++

	// Return the expected tool results as additional history context
	// (simulating the real agent loop appending tool results between steps)
	_ = req.Messages // available for assertions if needed

	return &types.ChatResponse{
		Content:          step.Content,
		ToolCalls:        step.ToolCalls,
		ReasoningContent: "",
	}, nil
}

func (m *MockLLM) Name() string {
	return m.name
}

// CallCount returns how many times Chat was called.
func (m *MockLLM) CallCount() int {
	return m.callCount
}

// MockTool returns a preset result without executing anything.
type MockTool struct {
	Name   string
	Result string
}

func (t *MockTool) ToolDef() types.ToolDef {
	return types.ToolDef{
		Name:        t.Name,
		Description: fmt.Sprintf("Mock tool: %s", t.Name),
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
	}
}

func (t *MockTool) Execute(ctx context.Context, args map[string]any) (string, error) {
	return t.Result, nil
}

// ToTool creates an agent.Tool from this MockTool.
func (t MockTool) ToTool() Tool {
	return Tool{
		Name:        t.Name,
		Description: "Mock tool: " + t.Name,
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Execute: t.Execute,
	}
}

// MockAgent creates an Agent pre-configured for testing.
func MockAgent(llm *MockLLM, steps []MockStep) *Agent {
	if llm == nil {
		llm = NewMockLLM(steps)
	}
	ag := &Agent{
		Provider:             llm,
		MaxSteps:             20,
		MaxTokens:            4096,
		CompressionThreshold: 0, // disabled by default
		SystemPrompt:         "You are a test agent.",
	}
	return ag
}
