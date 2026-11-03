package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/hook"
	"github.com/yusiwen/tinycode/types"
)

// TestHooksCommandListsWhatIsConfigured: the listing names each event with its
// match, so a user can see why a hook did or did not fire.
func TestHooksCommandListsWhatIsConfigured(t *testing.T) {
	m := newTestTUI()
	m.agent.Hooks = hook.New(config.HooksConfig{
		PostToolUse: []config.HookConfig{{Command: "gofmt -w .", Match: "*.go", TimeoutMS: 2000}},
		Stop:        []config.HookConfig{{Command: "echo done"}},
	}, t.TempDir())

	m.handleCommand("/hooks")

	reply := lastMessage(m)
	for _, want := range []string{"post_tool_use", "gofmt -w .", "*.go", "2s", "stop", "echo done"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply is missing %q:\n%s", want, reply)
		}
	}
	if strings.Contains(reply, "pre_tool_use") {
		t.Fatalf("an event with no hooks was listed:\n%s", reply)
	}
}

// TestHooksCommandWithoutConfiguration: the empty answer is instructions, not an
// empty table.
func TestHooksCommandWithoutConfiguration(t *testing.T) {
	m := newTestTUI()

	m.handleCommand("/hooks")

	reply := lastMessage(m)
	for _, want := range []string{"No hooks configured", "post_tool_use", "project-local"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply is missing %q:\n%s", want, reply)
		}
	}
}

var _ = types.RoleSystem

// TestExitFiresSessionEnd: both ways out of the TUI report the session's end, and
// the runner makes them one fire.
func TestExitFiresSessionEnd(t *testing.T) {
	dir := t.TempDir()
	marker := dir + "/end.txt"
	m := newTestTUI()
	m.agent.Hooks = hook.New(config.HooksConfig{SessionEnd: []config.HookConfig{
		{Command: "cat >> " + marker},
	}}, dir)

	m.handleCommand("/exit")

	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("session_end did not fire: %v", err)
	}
	if !strings.Contains(string(data), `"event":"session_end"`) {
		t.Fatalf("payload = %s", data)
	}
}
