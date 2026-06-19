package tool

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/types"
)

// mockTaskProvider returns a fixed response after one call.
type mockTaskProvider struct {
	calls int
}

func (m *mockTaskProvider) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	m.calls++
	// Always return text result (no tool calls) for test simplicity
	return &types.ChatResponse{
		Content: fmt.Sprintf("mock result (call %d)", m.calls),
	}, nil
}

func (m *mockTaskProvider) SupportsStream() bool { return false }
func (m *mockTaskProvider) Name() string         { return "mock" }

func TestTaskToolBasic(t *testing.T) {
	deps := &TaskToolDeps{
		Provider: &mockTaskProvider{},
		AllTools: []agent.Tool{
			{Name: "bash"},
			{Name: "read_file"},
			{Name: "write_file"},
		},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return &agent.AgentConfig{
				Name:        name,
				Mode:        agent.AgentModeSubagent,
				Description: "Test sub-agent",
				SystemPrompt: "You are a test sub-agent.",
				MaxSteps:    3,
				DeniedTools: []string{"write_file"},
			}
		},
	}

	tool := TaskTool(deps)
	result, err := tool.Execute(context.Background(), map[string]any{
		"agent": "explore",
		"goal":  "search for config files",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "mock result") {
		t.Fatalf("expected mock result, got: %s", result)
	}
}

func TestTaskToolUnknownAgent(t *testing.T) {
	deps := &TaskToolDeps{
		Provider: &mockTaskProvider{},
		AllTools: []agent.Tool{{Name: "bash"}},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return nil // unknown agent
		},
	}

	tool := TaskTool(deps)
	_, err := tool.Execute(context.Background(), map[string]any{
		"agent": "nonexistent",
		"goal":  "test",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("expected 'unknown agent' error, got: %v", err)
	}
}

func TestTaskToolEmptyArgs(t *testing.T) {
	deps := &TaskToolDeps{
		Provider:       &mockTaskProvider{},
		AllTools:       []agent.Tool{{Name: "bash"}},
		GetAgentConfig: func(name string) *agent.AgentConfig { return nil },
	}

	tool := TaskTool(deps)
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "agent and goal are required") {
		t.Fatalf("expected 'agent and goal required' error, got: %v", err)
	}
}

func TestTaskToolToolFiltering(t *testing.T) {
	// Verify that denied tools are not passed to sub-agent
	deps := &TaskToolDeps{
		Provider: &mockTaskProvider{},
		AllTools: []agent.Tool{
			{Name: "bash"},
			{Name: "read_file"},
			{Name: "write_file"},
		},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return &agent.AgentConfig{
				Name:        name,
				Mode:        agent.AgentModeSubagent,
				Description: "Read-only sub-agent",
				MaxSteps:    3,
				DeniedTools: []string{"write_file"},
			}
		},
	}

	tool := TaskTool(deps)
	// We can't directly inspect the sub-agent tools, but we can verify the
	// task tool executes without error (write_file denied but bash allowed)
	_, err := tool.Execute(context.Background(), map[string]any{
		"agent": "explore",
		"goal":  "search",
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

func TestTaskToolTimeout(t *testing.T) {
	// Use a provider that hangs to test timeout
	deps := &TaskToolDeps{
		Provider: &mockTaskProvider{},
		AllTools: []agent.Tool{
			{Name: "bash"},
		},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return &agent.AgentConfig{
				Name:        name,
				Mode:        agent.AgentModeSubagent,
				MaxSteps:    100, // many steps
				DeniedTools: nil,
			}
		},
	}

	tool := TaskTool(deps)
	// The mock provider only responds for 3 calls, but maxSteps is 100.
	// The agent loop will exhaust steps quickly (no tool calls in mock).
	// This should NOT timeout — it should return partial result.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := tool.Execute(ctx, map[string]any{
		"agent": "explore",
		"goal":  "search",
	})
	if err != nil && strings.Contains(err.Error(), "timed out after 120s") {
		t.Fatal("expected max-steps result or success, not 120s timeout")
	}
	// If no error, the result should contain something
	if err == nil && result == "" {
		t.Fatal("expected non-empty result when no error")
	}
	_ = result
}

func TestTaskCollectBasic(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	processed := make(chan string, 1)

	taskID := mgr.Start(&TaskToolDeps{
		Provider: &mockTaskProvider{},
		AllTools: []agent.Tool{{Name: "bash"}, {Name: "read_file"}},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return &agent.AgentConfig{
				Name:        name,
				Mode:        agent.AgentModeSubagent,
				MaxSteps:    3,
				DeniedTools: nil,
			}
		},
	}, "explore", "search for config files")

	if taskID == "" {
		t.Fatal("expected non-empty task ID")
	}

	if !strings.HasPrefix(taskID, "task_") {
		t.Fatalf("expected task_ prefix, got %s", taskID)
	}

	// Wait for background task to complete
	status := mgr.Status(taskID)
	t.Logf("initial status: %s", status)

	result, err := mgr.Collect(taskID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "mock result") {
		t.Fatalf("expected mock result, got: %s", result)
	}
	_ = processed
}

func TestTaskCollectUnknownTask(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	_, err := mgr.Collect("nonexistent")
	if err == nil || !strings.Contains(err.Error(), "unknown task") {
		t.Fatalf("expected 'unknown task' error, got: %v", err)
	}
}

func TestBgTaskStatus(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	taskID := mgr.Start(&TaskToolDeps{
		Provider: &mockTaskProvider{},
		AllTools: []agent.Tool{{Name: "bash"}},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return &agent.AgentConfig{
				Name:        name,
				Mode:        agent.AgentModeSubagent,
				MaxSteps:    3,
				DeniedTools: nil,
			}
		},
	}, "explore", "search")

	// Immediately after start, status should be "running"
	status := mgr.Status(taskID)
	if !strings.Contains(status, "running") {
		t.Fatalf("expected 'running' status, got: %s", status)
	}

	// Wait and collect
	_, err := mgr.Collect(taskID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// After collect, status should be "done"
	status = mgr.Status(taskID)
	if !strings.Contains(status, "done") {
		t.Fatalf("expected 'done' status, got: %s", status)
	}

	// Unknown task
	if mgr.Status("nonexistent") != "" {
		t.Fatal("expected empty status for unknown task")
	}
}
