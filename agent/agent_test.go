package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

func mockTool(name, result string) Tool {
	return MockTool{Name: name, Result: result}.ToTool()
}

func TestAgentSingleAnswer(t *testing.T) {
	llm := NewMockLLM([]MockStep{
		{Content: "Hello, world!"},
	})
	ag := MockAgent(llm, nil)
	ag.AddTool(mockTool("mock_tool", "ok"))

	result, err := ag.Run(context.Background(), "say hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Hello, world!" {
		t.Errorf("want 'Hello, world!', got %q", result)
	}
	if llm.CallCount() != 1 {
		t.Errorf("expected 1 LLM call, got %d", llm.CallCount())
	}
	if len(ag.History) != 2 {
		t.Errorf("expected 2 history entries, got %d", len(ag.History))
	}
}

func TestAgentOneToolCall(t *testing.T) {
	llm := NewMockLLM([]MockStep{
		{
			ToolCalls: []types.ToolCall{
				{ID: "call_1", Name: "mock_tool", Arguments: "{}"},
			},
		},
		{Content: "Final answer after tool."},
	})
	ag := MockAgent(llm, nil)
	ag.AddTool(mockTool("mock_tool", "tool result data"))

	result, err := ag.Run(context.Background(), "use a tool")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Final answer after tool." {
		t.Errorf("want 'Final answer after tool.', got %q", result)
	}
	if llm.CallCount() != 2 {
		t.Errorf("expected 2 LLM calls, got %d", llm.CallCount())
	}
}

func TestAgentMultipleToolCalls(t *testing.T) {
	llm := NewMockLLM([]MockStep{
		{
			ToolCalls: []types.ToolCall{
				{ID: "call_1", Name: "tool_a", Arguments: `{"x":"1"}`},
				{ID: "call_2", Name: "tool_b", Arguments: `{"y":"2"}`},
			},
		},
		{Content: "Done with both tools."},
	})
	ag := MockAgent(llm, nil)
	ag.AddTool(mockTool("tool_a", "result_a"))
	ag.AddTool(mockTool("tool_b", "result_b"))

	result, err := ag.Run(context.Background(), "use two tools")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "Done with both tools." {
		t.Errorf("want 'Done with both tools.', got %q", result)
	}
	if llm.CallCount() != 2 {
		t.Errorf("expected 2 LLM calls, got %d", llm.CallCount())
	}
}

func TestAgentToolNotFound(t *testing.T) {
	llm := NewMockLLM([]MockStep{
		{
			ToolCalls: []types.ToolCall{
				{ID: "call_1", Name: "nonexistent_tool", Arguments: "{}"},
			},
		},
		{Content: "Recovered."},
	})
	ag := MockAgent(llm, nil)

	result, err := ag.Run(context.Background(), "call missing tool")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Recovered") && result == "" {
		t.Errorf("expected a result, got empty")
	}
}

func TestAgentMaxSteps(t *testing.T) {
	var steps []MockStep
	for i := 0; i < 5; i++ {
		steps = append(steps, MockStep{
			ToolCalls: []types.ToolCall{
				{ID: fmt.Sprintf("call_%d", i), Name: "mock_tool", Arguments: "{}"},
			},
		})
	}
	llm := NewMockLLM(steps)
	ag := MockAgent(llm, nil)
	ag.MaxSteps = 2
	ag.AddTool(mockTool("mock_tool", "ok"))

	_, err := ag.Run(context.Background(), "loop")
	if err == nil {
		t.Fatal("expected error for exceeding max steps")
	}
	if !strings.Contains(err.Error(), "max steps") {
		t.Errorf("expected 'max steps' error, got %v", err)
	}
}

func TestAgentPreservesHistory(t *testing.T) {
	llm := NewMockLLM([]MockStep{
		{Content: "First answer"},
	})
	ag := MockAgent(llm, nil)
	ag.History = []types.Message{
		{Role: types.RoleUser, Content: "Previous question"},
		{Role: types.RoleAssistant, Content: "Previous answer"},
	}

	result, err := ag.Run(context.Background(), "New question")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "First answer" {
		t.Errorf("want 'First answer', got %q", result)
	}
	if len(ag.History) != 4 {
		t.Errorf("expected 4 history entries, got %d", len(ag.History))
	}
}

func TestAgentEmptyLLMResponse(t *testing.T) {
	llm := NewMockLLM([]MockStep{
		{Content: ""},
		{Content: "Retry worked."},
	})
	ag := MockAgent(llm, nil)
	ag.MaxSteps = 5

	result, err := ag.Run(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Retry") {
		t.Errorf("expected retry result, got %q", result)
	}
}

func TestAgentMultipleTurns(t *testing.T) {
	llm := NewMockLLM([]MockStep{
		{Content: "Answer one."},
	})
	ag := MockAgent(llm, nil)

	result1, err := ag.Run(context.Background(), "First question")
	if err != nil {
		t.Fatal(err)
	}
	if result1 != "Answer one." {
		t.Errorf("want 'Answer one.', got %q", result1)
	}

	llm2 := NewMockLLM([]MockStep{
		{Content: "Answer two."},
	})
	ag.Provider = llm2

	result2, err := ag.Run(context.Background(), "Second question")
	if err != nil {
		t.Fatal(err)
	}
	if result2 != "Answer two." {
		t.Errorf("want 'Answer two.', got %q", result2)
	}
	if len(ag.History) != 4 {
		t.Errorf("expected 4 history entries, got %d", len(ag.History))
	}
}
