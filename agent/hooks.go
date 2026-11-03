package agent

import (
	"context"
	"fmt"

	"github.com/yusiwen/tinycode/hook"
	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// Hook payloads carry enough for a hook to decide, and no more: a tool result can
// be megabytes, and a hook that needs the whole file can read it.
const (
	hookArgsLimit   = 500
	hookResultLimit = 1000
)

// clipText bounds one payload field.
func clipText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

// preToolHook fires the user's PreToolUse hooks and returns a refusal message
// when one of them denies the call. A refusal names the hook's own output, and it
// counts as a failed call for the repeat guard: a model that repeats a refused
// call is answered with the same refusal, not with another execution.
func (a *Agent) preToolHook(ctx context.Context, toolName, args string, scope types.ToolCallScope, step int) string {
	if a.Hooks == nil {
		return ""
	}
	result := a.Hooks.Fire(ctx, hook.PreToolUse, hook.Payload{
		Tool:    toolName,
		Args:    clipText(args, hookArgsLimit),
		Session: scope.Session,
		Step:    step,
	})
	if result.Err != nil {
		tlog.Warn("agent.hooks", "post hook failed", "tool", toolName, "error", result.Err)
	}
	if !result.Denied {
		return ""
	}
	return fmt.Sprintf("tool call refused by a hook (%s): %s", toolName, result.Reason)
}

// stopHook fires the user's Stop hooks when a run ends. It reports the answer or
// the error, never blocks the run's return, and uses a background context because
// the run's own may already be cancelled.
func (a *Agent) stopHook(answer string, runErr error) {
	payload := hook.Payload{Result: clipText(answer, hookResultLimit)}
	if runErr != nil {
		payload.Error = runErr.Error()
	}
	if fired := a.Hooks.Fire(context.Background(), hook.Stop, payload); fired.Err != nil {
		tlog.Warn("agent.hooks", "stop hook failed", "error", fired.Err)
	}
}

// postToolHook fires the user's PostToolUse hooks. Their failures are logged and
// never change the tool's result: a formatter that breaks is a fact about the
// hook, not an error the model should try to fix by calling the tool again.
func (a *Agent) postToolHook(ctx context.Context, toolName, args, result string, execErr error, scope types.ToolCallScope, step int) {
	if a.Hooks == nil {
		return
	}
	payload := hook.Payload{
		Tool:    toolName,
		Args:    clipText(args, hookArgsLimit),
		Result:  clipText(result, hookResultLimit),
		Session: scope.Session,
		Step:    step,
	}
	if execErr != nil {
		payload.Error = execErr.Error()
	}
	if fired := a.Hooks.Fire(ctx, hook.PostToolUse, payload); fired.Err != nil {
		tlog.Warn("agent.hooks", "post hook failed", "tool", toolName, "error", fired.Err)
	}
}
