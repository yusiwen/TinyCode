package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/hook"
	"github.com/yusiwen/tinycode/types"
)

// hookRun drives one run that calls the echo tool once and then answers. It
// returns the provider (for its recorded requests) and how often the tool ran.
func hookRun(t *testing.T, hooks config.HooksConfig, session string) (*scriptedProvider, int) {
	t.Helper()
	dir := t.TempDir()
	provider := &scriptedProvider{steps: []types.ChatResponse{{
		ToolCalls: []types.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}},
	}}}
	executed := 0
	ag := New(provider)
	ag.MaxSteps = 3
	ag.Tools = []Tool{{
		Name: "echo", Description: "echo", Parameters: map[string]any{"type": "object"},
		Execute: func(context.Context, map[string]any) (string, error) {
			executed++
			return "echoed output", nil
		},
	}}
	ag.Hooks = hook.New(hooks, dir)
	ctx := context.Background()
	if session != "" {
		ctx = types.WithToolCallScope(ctx, types.ToolCallScope{Session: session})
	}
	if _, err := ag.Run(ctx, "do it"); err != nil {
		t.Fatalf("run: %v", err)
	}
	return provider, executed
}

// TestPreToolUseHookRefusesTheCall: the model reads the refusal and the tool
// never runs.
func TestPreToolUseHookRefusesTheCall(t *testing.T) {
	provider, executed := hookRun(t, config.HooksConfig{PreToolUse: []config.HookConfig{
		{Command: "echo 'not this file'; exit 2", Match: "echo"},
	}}, "")

	if executed != 0 {
		t.Fatalf("the tool ran %d time(s) despite the refusal", executed)
	}
	if len(provider.requests) < 2 {
		t.Fatalf("the run stopped after %d request(s) instead of reporting the refusal", len(provider.requests))
	}
	if !containsMessage(provider.requests[1].Messages, "refused by a hook") {
		t.Fatalf("the refusal did not reach the model: %+v", provider.requests[1].Messages)
	}
}

// TestPostToolUseHookSeesTheResult: the hook runs after the call and receives
// the session the loop was given.
func TestPostToolUseHookSeesTheResult(t *testing.T) {
	dir := t.TempDir()
	payloadFile := filepath.Join(dir, "payload.json")
	provider := &scriptedProvider{steps: []types.ChatResponse{{
		ToolCalls: []types.ToolCall{{ID: "c1", Name: "echo", Arguments: `{"text":"hi"}`}},
	}}}
	ag := New(provider)
	ag.MaxSteps = 3
	ag.Tools = []Tool{{
		Name: "echo", Description: "echo", Parameters: map[string]any{"type": "object"},
		Execute: func(context.Context, map[string]any) (string, error) { return "echoed output", nil },
	}}
	ag.Hooks = hook.New(config.HooksConfig{PostToolUse: []config.HookConfig{
		{Command: "cat > " + payloadFile},
	}}, dir)

	ctx := types.WithToolCallScope(context.Background(), types.ToolCallScope{Session: "s9"})
	if _, err := ag.Run(ctx, "do it"); err != nil {
		t.Fatalf("run: %v", err)
	}
	data, err := os.ReadFile(payloadFile)
	if err != nil {
		t.Fatalf("the post hook did not run: %v", err)
	}
	for _, want := range []string{`"event":"post_tool_use"`, `"tool":"echo"`, `"session":"s9"`, "echoed output"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("payload is missing %q: %s", want, data)
		}
	}
}

// TestHooksDoNotRunWithoutAConfig: the loop must not pay for an empty runner.
func TestHooksDoNotRunWithoutAConfig(t *testing.T) {
	provider, executed := hookRun(t, config.HooksConfig{}, "")
	if executed != 1 {
		t.Fatalf("the tool ran %d time(s), want 1", executed)
	}
	if len(provider.requests) < 2 || containsMessage(provider.requests[1].Messages, "hook") {
		t.Fatalf("a hook message appeared without hooks: %+v", provider.requests[1].Messages)
	}
}

// TestAgentWithoutHooksIsUnaffected: nil is the common case.
func TestAgentWithoutHooksIsUnaffected(t *testing.T) {
	provider := &scriptedProvider{}
	ag := New(provider)
	ag.MaxSteps = 2
	if _, err := ag.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if ag.Hooks != nil {
		t.Fatal("a fresh agent carries hooks")
	}
}

// TestStopHookFiresOnEveryOutcome: a finished run and a failed run both report,
// and a cancelled one still does.
func TestStopHookFiresOnEveryOutcome(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		fail    bool
	}{
		{name: "final answer", content: "all done"},
		{name: "failure", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "stop.json")
			provider := &scriptedProvider{}
			if tc.fail {
				provider = &scriptedProvider{fail: true}
			} else {
				provider = &scriptedProvider{steps: []types.ChatResponse{{Content: tc.content}}}
			}
			ag := New(provider)
			ag.MaxSteps = 2
			ag.Hooks = hook.New(config.HooksConfig{Stop: []config.HookConfig{
				{Command: "cat > " + marker},
			}}, dir)

			_, _ = ag.Run(context.Background(), "do it")

			data, err := os.ReadFile(marker)
			if err != nil {
				t.Fatalf("the stop hook did not run: %v", err)
			}
			if tc.fail && !strings.Contains(string(data), `"error":`) {
				t.Errorf("a failed run's stop payload has no error: %s", data)
			}
			if !tc.fail && !strings.Contains(string(data), tc.content) {
				t.Errorf("the stop payload is missing the answer: %s", data)
			}
		})
	}
}
