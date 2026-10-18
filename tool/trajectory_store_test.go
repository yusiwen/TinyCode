package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// TestTrajectoryStoreIsNotAToolPath pins the rule the checkpoint store has for
// the same reason: the record is what a run is measured by, so a tool that can
// write it makes the measurement worthless — even where a user's "always allow"
// on the directory around it would open it. Full access is the documented
// exception: that mode means no boundary at all.
func TestTrajectoryStoreIsNotAToolPath(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, ".tinycode", "trajectories")
	if err := os.MkdirAll(filepath.Join(store, "20261018T070422Z-r1.jsonl"), 0o755); err != nil {
		t.Fatal(err)
	}
	SetTrajectoryRoot(store)
	t.Cleanup(func() { SetTrajectoryRoot("") })

	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{ProjectRoot: root, allowedPaths: map[string]bool{}}
	t.Cleanup(func() { DefaultSandbox = saved })

	target := filepath.Join(store, "20261018T070422Z-r1.jsonl")
	_, denied, err := CheckPathAccess(context.Background(), target)
	if err != nil {
		t.Fatalf("check path: %v", err)
	}
	if denied == "" {
		t.Fatal("a tool path beneath the trajectory store must be refused")
	}
	for _, want := range []string{types.RefusalMarker, "trajectory store", "ask: " + types.AskReserved} {
		if !strings.Contains(denied, want) {
			t.Errorf("the refusal is missing %q:\n%s", want, denied)
		}
	}

	// The control: the same directory is ordinary once recording is off, so the
	// rule is about the store rather than about the name.
	SetTrajectoryRoot("")
	if _, controlDenied, err := CheckPathAccess(context.Background(), target); err != nil {
		t.Fatalf("check path with no store: %v", err)
	} else if controlDenied != "" {
		t.Fatalf("a path was refused with no trajectory store configured: %s", controlDenied)
	}

	SetTrajectoryRoot(store)
	full := types.WithSandboxPolicy(context.Background(), types.SandboxPolicy{
		Mode: types.SandboxFullAccess, ProjectRoot: root,
	})
	if _, fullDenied, err := CheckPathAccess(full, target); err != nil || fullDenied != "" {
		t.Errorf("full access must not fence the store: denied=%q err=%v", fullDenied, err)
	}
}
