package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
	"github.com/yusiwen/tinycode/verify"
)

// testRun drives one run that calls a write tool once and then answers. It
// returns the provider so a test can read what the model was sent.
func testRun(t *testing.T, auto bool, command string) *scriptedProvider {
	t.Helper()
	provider := &scriptedProvider{steps: []types.ChatResponse{{
		ToolCalls: []types.ToolCall{{ID: "c1", Name: "write_file", Arguments: `{"path":"a.go"}`}},
	}}}
	ag := New(provider)
	ag.MaxSteps = 3
	ag.Tools = []Tool{{
		Name: "write_file", Description: "write", Parameters: map[string]any{"type": "object"},
		Execute: func(context.Context, map[string]any) (string, error) { return "wrote a.go", nil },
	}}
	ag.Tests = &verify.Settings{Dir: t.TempDir(), Config: verify.Config{Command: command}}
	ag.TestAfterWrite = auto
	if _, err := ag.Run(context.Background(), "write it"); err != nil {
		t.Fatalf("run: %v", err)
	}
	return provider
}

// TestAutoAfterWriteFeedsFailuresBack: the failing suite appears in the tool
// result, which is what the model reads next.
func TestAutoAfterWriteFeedsFailuresBack(t *testing.T) {
	provider := testRun(t, true, "echo '--- FAIL: TestBroken'; exit 1")

	if len(provider.requests) < 2 {
		t.Fatalf("the run stopped after %d request(s)", len(provider.requests))
	}
	next := provider.requests[1]
	if !containsMessage(next.Messages, "[tests] tests failed") || !containsMessage(next.Messages, "TestBroken") {
		t.Fatalf("the failure did not reach the model: %+v", next.Messages)
	}
}

// TestAutoAfterWriteOffRunsNothing: the default costs nothing.
func TestAutoAfterWriteOffRunsNothing(t *testing.T) {
	provider := testRun(t, false, "echo should-not-run")

	if len(provider.requests) < 2 {
		t.Fatalf("the run stopped after %d request(s)", len(provider.requests))
	}
	if containsMessage(provider.requests[1].Messages, "[tests]") {
		t.Fatalf("tests ran without the setting: %+v", provider.requests[1].Messages)
	}
}

// TestAutoAfterWriteOnlyForWriteTools: a read does not invalidate a test result.
func TestAutoAfterWriteOnlyForWriteTools(t *testing.T) {
	provider := &scriptedProvider{steps: []types.ChatResponse{{
		ToolCalls: []types.ToolCall{{ID: "c1", Name: "read_file", Arguments: `{"path":"a.go"}`}},
	}}}
	ag := New(provider)
	ag.MaxSteps = 3
	ag.Tools = []Tool{{
		Name: "read_file", Description: "read", Parameters: map[string]any{"type": "object"},
		Execute: func(context.Context, map[string]any) (string, error) { return "contents", nil },
	}}
	ag.Tests = &verify.Settings{Dir: t.TempDir(), Config: verify.Config{Command: "echo should-not-run"}}
	ag.TestAfterWrite = true

	if _, err := ag.Run(context.Background(), "read it"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if containsMessage(provider.requests[1].Messages, "[tests]") {
		t.Fatalf("tests ran after a read: %+v", provider.requests[1].Messages)
	}
}

// TestAutoAfterWriteWithoutACommand: the write still succeeds, and the result says
// how to configure a command.
func TestAutoAfterWriteWithoutACommand(t *testing.T) {
	provider := testRun(t, true, "")

	if !containsMessage(provider.requests[1].Messages, "[tests] no test command found") {
		t.Fatalf("the missing command was not reported: %+v", provider.requests[1].Messages)
	}
}

// TestTimeoutOf: the config's milliseconds become the runner's bound.
func TestTimeoutOf(t *testing.T) {
	if got := testTimeout(0); got != 0 {
		t.Fatalf("testTimeout(0) = %s", got)
	}
	if got := testTimeout(1500); got.Seconds() != 1.5 {
		t.Fatalf("testTimeout(1500) = %s", got)
	}
}

var _ = strings.Contains
