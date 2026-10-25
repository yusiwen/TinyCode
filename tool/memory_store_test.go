package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// TestMemoryStoreIsNotAToolPath pins the rule the checkpoint and trajectory
// stores carry: the memories every later run may recall are the user's, so a
// tool that can write them could seed the next session — even where an "always
// allow" on the directory around the store would open it. Full access is the
// documented exception: that mode means no boundary at all.
func TestMemoryStoreIsNotAToolPath(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, ".tinycode", "memory")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	SetMemoryRoot(store)
	t.Cleanup(func() { SetMemoryRoot("") })

	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{ProjectRoot: root, allowedPaths: map[string]bool{}}
	t.Cleanup(func() { DefaultSandbox = saved })

	target := filepath.Join(store, "abc123.jsonl")
	_, denied, err := CheckPathAccess(context.Background(), target)
	if err != nil {
		t.Fatalf("check path: %v", err)
	}
	if denied == "" {
		t.Fatal("a tool path beneath the memory store must be refused")
	}
	for _, want := range []string{types.RefusalMarker, "memory store", "ask: " + types.AskReserved} {
		if !strings.Contains(denied, want) {
			t.Errorf("the refusal is missing %q:\n%s", want, denied)
		}
	}

	// The control: the same directory is ordinary once memory is off, so the rule
	// is about the store rather than about the name.
	SetMemoryRoot("")
	if _, controlDenied, err := CheckPathAccess(context.Background(), target); err != nil {
		t.Fatalf("check path with no store: %v", err)
	} else if controlDenied != "" {
		t.Fatalf("a path was refused with no memory store configured: %s", controlDenied)
	}

	SetMemoryRoot(store)
	full := types.WithSandboxPolicy(context.Background(), types.SandboxPolicy{
		Mode: types.SandboxFullAccess, ProjectRoot: root,
	})
	if _, fullDenied, err := CheckPathAccess(full, target); err != nil || fullDenied != "" {
		t.Errorf("full access must not fence the store: denied=%q err=%v", fullDenied, err)
	}
}
