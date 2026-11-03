package hook

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/config"
)

// runner builds a runner whose hooks run in a throwaway project directory.
func runner(t *testing.T, hooks config.HooksConfig) (*Runner, string) {
	t.Helper()
	dir := t.TempDir()
	return New(hooks, dir), dir
}

// TestPostToolUseReceivesThePayload: the hook gets the event and the tool on
// stdin as one JSON object.
func TestPostToolUseReceivesThePayload(t *testing.T) {
	r, dir := runner(t, config.HooksConfig{PostToolUse: []config.HookConfig{
		{Command: "cat > payload.json"},
	}})

	result := r.Fire(context.Background(), PostToolUse, Payload{Tool: "write_file", Args: `{"path":"a.go"}`})
	if result.Ran != 1 || result.Err != nil || result.Denied {
		t.Fatalf("result = %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(dir, "payload.json"))
	if err != nil {
		t.Fatalf("the hook did not run: %v", err)
	}
	var payload Payload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("payload is not JSON: %v\n%s", err, data)
	}
	if payload.Event != string(PostToolUse) || payload.Tool != "write_file" || payload.Args != `{"path":"a.go"}` {
		t.Fatalf("payload = %+v", payload)
	}
}

// TestPreToolUseDeniesOnANonZeroExit: the hook's own output is the reason, and
// the call is not executed by anyone else.
func TestPreToolUseDeniesOnANonZeroExit(t *testing.T) {
	r, _ := runner(t, config.HooksConfig{PreToolUse: []config.HookConfig{
		{Command: "echo 'no writes to generated files'; exit 3"},
	}})

	result := r.Fire(context.Background(), PreToolUse, Payload{Tool: "write_file"})
	if !result.Denied {
		t.Fatalf("a non-zero exit did not deny: %+v", result)
	}
	if !strings.Contains(result.Reason, "no writes to generated files") {
		t.Fatalf("reason = %q", result.Reason)
	}
}

// TestPreToolUseDeniesWhenTheHookCannotRunOrHangs: a guard that fails open is not
// a guard.
func TestPreToolUseDeniesWhenTheHookCannotRunOrHangs(t *testing.T) {
	r, _ := runner(t, config.HooksConfig{PreToolUse: []config.HookConfig{
		// The trailing command keeps the shell alive, so the `sleep` is a child
		// that inherits the output pipe — the case WaitDelay exists for, and the
		// one that failed on Linux while passing on macOS (where `sh -c` execs a
		// single command and the kill reaches it).
		{Command: "sleep 5; true", TimeoutMS: 50},
	}})
	start := time.Now()
	result := r.Fire(context.Background(), PreToolUse, Payload{Tool: "bash"})
	if !result.Denied {
		t.Fatalf("a hanging hook did not deny: %+v", result)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the timeout took %s, want about the configured 50ms", elapsed)
	}
	if !strings.Contains(result.Reason, "timed out") {
		t.Fatalf("reason = %q", result.Reason)
	}
}

// TestPostToolUseReportsAFailureWithoutStopping: a formatter that breaks is a
// fact, not a wall.
func TestPostToolUseReportsAFailureWithoutStopping(t *testing.T) {
	r, _ := runner(t, config.HooksConfig{PostToolUse: []config.HookConfig{
		{Command: "echo boom >&2; exit 1"},
	}})

	result := r.Fire(context.Background(), PostToolUse, Payload{Tool: "write_file"})
	if result.Denied {
		t.Fatal("a post hook must not deny")
	}
	if result.Err == nil || !strings.Contains(result.Err.Error(), "boom") {
		t.Fatalf("err = %v", result.Err)
	}
}

// TestMatchScopesAHookToATool: a `bash*` hook does not fire for read_file.
func TestMatchScopesAHookToATool(t *testing.T) {
	r, dir := runner(t, config.HooksConfig{PostToolUse: []config.HookConfig{
		{Command: "echo bash >> ran.txt", Match: "bash*"},
	}})

	r.Fire(context.Background(), PostToolUse, Payload{Tool: "read_file"})
	if _, err := os.Stat(filepath.Join(dir, "ran.txt")); err == nil {
		t.Fatal("a mismatched hook ran")
	}
	r.Fire(context.Background(), PostToolUse, Payload{Tool: "bash"})
	data, err := os.ReadFile(filepath.Join(dir, "ran.txt"))
	if err != nil || !strings.Contains(string(data), "bash") {
		t.Fatalf("the matched hook did not run: %v %q", err, data)
	}
}

// TestOutputIsTruncated: a hook that prints megabytes costs a bounded transcript.
func TestOutputIsTruncated(t *testing.T) {
	r, _ := runner(t, config.HooksConfig{PostToolUse: []config.HookConfig{
		{Command: "head -c 100000 /dev/zero | tr '\\0' 'x'"},
	}})

	result := r.Fire(context.Background(), PostToolUse, Payload{Tool: "bash"})
	if len(result.Output) > maxHookOutput+len("… (hook output truncated)") {
		t.Fatalf("output is %d bytes, want it truncated", len(result.Output))
	}
}

// TestEmptyRunnerDoesNothing: no configuration costs no process.
func TestEmptyRunnerDoesNothing(t *testing.T) {
	r, _ := runner(t, config.HooksConfig{})
	if !r.Empty() {
		t.Fatal("an empty config reported hooks")
	}
	if result := r.Fire(context.Background(), PreToolUse, Payload{Tool: "bash"}); result.Ran != 0 || result.Denied {
		t.Fatalf("result = %+v", result)
	}
}

// TestNilRunnerIsSafe: a session without hooks must not need a nil check
// everywhere.
func TestNilRunnerIsSafe(t *testing.T) {
	var r *Runner
	if !r.Empty() {
		t.Fatal("a nil runner reported hooks")
	}
	if result := r.Fire(context.Background(), Stop, Payload{}); result.Ran != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// TestTimeoutIsBoundedByConfig pins the clamp: a hook cannot ask for an hour.
func TestTimeoutIsBoundedByConfig(t *testing.T) {
	if got := (config.HookConfig{}).Timeout(); got != config.DefaultHookTimeoutMS*time.Millisecond {
		t.Fatalf("default timeout = %s", got)
	}
	if got := (config.HookConfig{TimeoutMS: 10 * config.MaxHookTimeoutMS}).Timeout(); got != config.MaxHookTimeoutMS*time.Millisecond {
		t.Fatalf("clamped timeout = %s", got)
	}
}
