package config

import (
	"testing"
	"time"
)

// TestResolveHooksKeepsTheUsersOwnFile: hooks are commands that run by
// themselves, so the user's file is the only one that counts.
func TestResolveHooksKeepsTheUsersOwnFile(t *testing.T) {
	layers := Layers{
		User: UserLayer{cfg: Config{Hooks: &HooksConfig{PostToolUse: []HookConfig{
			{Command: "gofmt -w .", Match: "*.go"},
		}}}},
		Project: ProjectLayer{},
	}

	hooks, ignored := layers.ResolveHooks()
	if ignored {
		t.Fatal("a project layer without hooks was reported as ignored")
	}
	if len(hooks.PostToolUse) != 1 || hooks.PostToolUse[0].Command != "gofmt -w ." {
		t.Fatalf("hooks = %+v", hooks)
	}
}

// TestResolveHooksDropsAProjectSection: a cloned repository may not add a hook,
// and the drop is reported so the person who wrote it can see it did not take.
func TestResolveHooksDropsAProjectSection(t *testing.T) {
	projectOnly := Layers{
		Project: ProjectLayer{cfg: Config{Hooks: &HooksConfig{PreToolUse: []HookConfig{
			{Command: "echo pwned"},
		}}}},
	}
	hooks, ignored := projectOnly.ResolveHooks()
	if !ignored {
		t.Fatal("a project-local hooks section was not reported")
	}
	if len(hooks.PreToolUse)+len(hooks.PostToolUse)+len(hooks.Stop)+len(hooks.SessionEnd) != 0 {
		t.Fatalf("a project hook survived: %+v", hooks)
	}

	both := Layers{
		User:    UserLayer{cfg: Config{Hooks: &HooksConfig{Stop: []HookConfig{{Command: "echo done"}}}}},
		Project: ProjectLayer{cfg: Config{Hooks: &HooksConfig{Stop: []HookConfig{{Command: "echo project"}}}}},
	}
	hooks, ignored = both.ResolveHooks()
	if !ignored || len(hooks.Stop) != 1 || hooks.Stop[0].Command != "echo done" {
		t.Fatalf("hooks = %+v ignored=%v", hooks, ignored)
	}
}

// TestResolveHooksDropsAnEmptyCommand: an entry with no command could never run.
func TestResolveHooksDropsAnEmptyCommand(t *testing.T) {
	layers := Layers{User: UserLayer{cfg: Config{Hooks: &HooksConfig{
		PreToolUse: []HookConfig{{Command: "  "}, {Command: "true"}},
	}}}}
	hooks, _ := layers.ResolveHooks()
	if len(hooks.PreToolUse) != 1 || hooks.PreToolUse[0].Command != "true" {
		t.Fatalf("hooks = %+v", hooks)
	}
}

// TestHookTimeoutIsBounded pins the default and the ceiling.
func TestHookTimeoutIsBounded(t *testing.T) {
	if got := (HookConfig{}).Timeout(); got != DefaultHookTimeoutMS*time.Millisecond {
		t.Fatalf("default = %s", got)
	}
	if got := (HookConfig{TimeoutMS: MaxHookTimeoutMS * 2}).Timeout(); got != MaxHookTimeoutMS*time.Millisecond {
		t.Fatalf("clamped = %s", got)
	}
}
