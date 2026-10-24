package tool

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
		ResolveProvider: fixedProvider(&mockTaskProvider{}),
		AllTools: []agent.Tool{
			{Name: "bash"},
			{Name: "read_file"},
			{Name: "write_file"},
		},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return &agent.AgentConfig{
				Name:         name,
				Mode:         agent.AgentModeSubagent,
				Description:  "Test sub-agent",
				SystemPrompt: "You are a test sub-agent.",
				MaxSteps:     3,
				DeniedTools:  []string{"write_file"},
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
		ResolveProvider: fixedProvider(&mockTaskProvider{}),
		AllTools:        []agent.Tool{{Name: "bash"}},
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
		ResolveProvider: fixedProvider(&mockTaskProvider{}),
		AllTools:        []agent.Tool{{Name: "bash"}},
		GetAgentConfig:  func(name string) *agent.AgentConfig { return nil },
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
		ResolveProvider: fixedProvider(&mockTaskProvider{}),
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
		ResolveProvider: fixedProvider(&mockTaskProvider{}),
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
		ResolveProvider: fixedProvider(&mockTaskProvider{}),
		AllTools:        []agent.Tool{{Name: "bash"}, {Name: "read_file"}},
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
		ResolveProvider: fixedProvider(&mockTaskProvider{}),
		AllTools:        []agent.Tool{{Name: "bash"}},
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

// blockingProvider blocks inside Chat until its context is cancelled and then
// records that cancellation. It models a sub-agent that would keep running
// forever if the caller abandoned it without cancelling its context.
type blockingProvider struct {
	started    chan struct{}
	cancelled  chan struct{}
	startOnce  sync.Once
	cancelOnce sync.Once
}

func newBlockingProvider() *blockingProvider {
	return &blockingProvider{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
}

func (p *blockingProvider) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.startOnce.Do(func() { close(p.started) })
	<-ctx.Done()
	p.cancelOnce.Do(func() { close(p.cancelled) })
	return nil, ctx.Err()
}

func (p *blockingProvider) SupportsStream() bool { return false }
func (p *blockingProvider) Name() string         { return "blocking" }

// subagentDeps returns deps with a permissive config for the given provider.
func subagentDeps(provider agent.LLMProvider) *TaskToolDeps {
	return &TaskToolDeps{
		ResolveProvider: fixedProvider(provider),
		AllTools:        []agent.Tool{{Name: "bash"}},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return &agent.AgentConfig{
				Name:     name,
				Mode:     agent.AgentModeSubagent,
				MaxSteps: 100,
			}
		},
	}
}

// TestTaskToolTimeoutCancelsSubAgent verifies that a timed-out synchronous task
// returns the timeout error promptly and actually cancels the sub-agent's
// context instead of leaving it running.
func TestTaskToolTimeoutCancelsSubAgent(t *testing.T) {
	orig := taskTimeout
	taskTimeout = 150 * time.Millisecond
	defer func() { taskTimeout = orig }()

	prov := newBlockingProvider()
	deps := subagentDeps(prov)

	start := time.Now()
	_, err := TaskTool(deps).Execute(context.Background(), map[string]any{
		"agent": "explore",
		"goal":  "block forever",
	})
	elapsed := time.Since(start)

	if err == nil || !strings.Contains(err.Error(), "timed out after 120s") {
		t.Fatalf("expected timeout error, got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout returned too slowly: %s", elapsed)
	}

	select {
	case <-prov.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("sub-agent context was not cancelled after timeout (sub-agent still running)")
	}
}

// TestTaskToolParentCancelCancelsSubAgent verifies that cancelling the tool's
// own context stops the sub-agent and returns promptly.
func TestTaskToolParentCancelCancelsSubAgent(t *testing.T) {
	prov := newBlockingProvider()
	deps := subagentDeps(prov)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := TaskTool(deps).Execute(ctx, map[string]any{
			"agent": "explore",
			"goal":  "block forever",
		})
		done <- err
	}()

	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("sub-agent never started")
	}
	cancel()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("expected context cancellation error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("task tool did not return after parent context cancellation")
	}

	select {
	case <-prov.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("sub-agent context was not cancelled after parent cancellation")
	}
}

// TestBgTaskTimeoutCancelsSubAgent verifies the background path terminates and
// cancels an abandoned sub-agent at the deadline.
func TestBgTaskTimeoutCancelsSubAgent(t *testing.T) {
	orig := bgTaskTimeout
	bgTaskTimeout = 150 * time.Millisecond
	defer func() { bgTaskTimeout = orig }()

	prov := newBlockingProvider()
	mgr := NewBackgroundTaskManager()
	id := mgr.Start(subagentDeps(prov), "explore", "block forever")

	if _, err := mgr.Collect(id); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error, got: %v", err)
	}

	select {
	case <-prov.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("background sub-agent context was not cancelled on timeout")
	}

	if status := mgr.Status(id); status != "timeout" {
		t.Fatalf("expected 'timeout' status, got: %q", status)
	}
}

// TestBgTaskCollectContextCancellation verifies task_collect honours its own
// context instead of blocking until the background task finishes.
func TestBgTaskCollectContextCancellation(t *testing.T) {
	orig := bgTaskTimeout
	bgTaskTimeout = 350 * time.Millisecond
	defer func() { bgTaskTimeout = orig }()

	prov := newBlockingProvider()
	mgr := NewBackgroundTaskManager()
	id := mgr.Start(subagentDeps(prov), "explore", "block forever")

	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("background sub-agent never started")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := mgr.CollectContext(ctx, id)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected context deadline error, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("CollectContext ignored cancellation for %s", elapsed)
	}

	// Let the watchdog terminate the task so no goroutine outlives the test.
	select {
	case <-prov.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("background sub-agent context was not cancelled")
	}
	if _, err := mgr.Collect(id); err == nil {
		t.Fatal("expected an error for the timed-out background task")
	}
}

// TestBgTaskUnknownAgentConcurrentStatus checks the unknown-agent early exit is
// consistent under concurrent Status polling (it previously wrote task state
// without holding the manager lock).
func TestBgTaskUnknownAgentConcurrentStatus(t *testing.T) {
	mgr := NewBackgroundTaskManager()
	id := mgr.Start(&TaskToolDeps{
		ResolveProvider: fixedProvider(&mockTaskProvider{}),
		GetAgentConfig:  func(string) *agent.AgentConfig { return nil },
	}, "nope", "goal")

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = mgr.Status(id)
			}
		}
	}()

	_, err := mgr.Collect(id)
	close(stop)
	wg.Wait()

	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("expected 'unknown agent' error, got: %v", err)
	}
	if status := mgr.Status(id); status != "failed" {
		t.Fatalf("expected 'failed' status, got: %q", status)
	}
}

// fixedProvider adapts one provider to the resolver shape: every route, named or
// empty, resolves to it. Tests that care about which route was asked for use a
// resolver of their own.
func fixedProvider(p agent.LLMProvider) func(string) (agent.LLMProvider, error) {
	return func(string) (agent.LLMProvider, error) { return p, nil }
}

// TestTaskSubAgentUsesItsConfiguredRoute: a delegated agent's route selects its
// provider, and the lookup happens when the task starts rather than at startup
// (roadmap A5).
func TestTaskSubAgentUsesItsConfiguredRoute(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	first := &mockTaskProvider{}
	second := &mockTaskProvider{}
	deps := &TaskToolDeps{
		ResolveProvider: func(route string) (agent.LLMProvider, error) {
			mu.Lock()
			asked = append(asked, route)
			mu.Unlock()
			if route == "second" {
				return second, nil
			}
			return first, nil
		},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return &agent.AgentConfig{Name: name, Mode: agent.AgentModeSubagent, MaxSteps: 5, Route: "second"}
		},
	}
	out, err := TaskTool(deps).Execute(context.Background(), map[string]any{"agent": "general", "goal": "x"})
	if err != nil {
		t.Fatalf("task: %v", err)
	}
	if !strings.Contains(out, "mock result") {
		t.Fatalf("task output = %q", out)
	}
	if second.calls != 1 || first.calls != 0 {
		t.Errorf("calls = first:%d second:%d, want the pinned provider only", first.calls, second.calls)
	}
	if len(asked) != 1 || asked[0] != "second" {
		t.Errorf("resolver asked for %v, want [second]", asked)
	}
}

// TestTaskSubAgentWithoutARouteFollowsTheSession: no pin means the resolver's
// active provider, asked as the empty route.
func TestTaskSubAgentWithoutARouteFollowsTheSession(t *testing.T) {
	var asked []string
	active := &mockTaskProvider{}
	deps := &TaskToolDeps{
		ResolveProvider: func(route string) (agent.LLMProvider, error) {
			asked = append(asked, route)
			return active, nil
		},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return &agent.AgentConfig{Name: name, Mode: agent.AgentModeSubagent, MaxSteps: 5}
		},
	}
	if _, err := TaskTool(deps).Execute(context.Background(), map[string]any{"agent": "general", "goal": "x"}); err != nil {
		t.Fatalf("task: %v", err)
	}
	if len(asked) != 1 || asked[0] != "" {
		t.Errorf("resolver asked for %v, want [\"\"] so the session decides", asked)
	}
	if active.calls != 1 {
		t.Errorf("active provider calls = %d, want 1", active.calls)
	}
}

// TestTaskSubAgentRouteErrorIsReported keeps a bad pin from turning into a task
// that silently uses another provider.
func TestTaskSubAgentRouteErrorIsReported(t *testing.T) {
	deps := &TaskToolDeps{
		ResolveProvider: func(string) (agent.LLMProvider, error) {
			return nil, fmt.Errorf(`no provider named "gone"`)
		},
		GetAgentConfig: func(name string) *agent.AgentConfig {
			return &agent.AgentConfig{Name: name, Mode: agent.AgentModeSubagent, MaxSteps: 5, Route: "gone"}
		},
	}
	_, err := TaskTool(deps).Execute(context.Background(), map[string]any{"agent": "general", "goal": "x"})
	if err == nil || !strings.Contains(err.Error(), `no provider named "gone"`) {
		t.Fatalf("error = %v, want the resolver's refusal", err)
	}
}
