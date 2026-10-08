package tool

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// policyCtx attaches a policy that grants exactly one root.
func policyCtx(mode types.SandboxMode, root string) context.Context {
	return types.WithSandboxPolicy(context.Background(), types.SandboxPolicy{
		Mode:        mode,
		ProjectRoot: root,
		Roots:       []string{root},
		Source:      "test",
	})
}

// TestPolicyIsFrozenAtRunStart pins what "per-call policy" buys: a run that has
// started is governed by the roots it was given, no matter what the
// configuration says afterwards.
func TestPolicyIsFrozenAtRunStart(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()

	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{ProjectRoot: first, allowedPaths: map[string]bool{}}
	defer func() { DefaultSandbox = saved }()

	ctx := types.WithSandboxPolicy(context.Background(), PolicyFor(types.SandboxWorkspaceWrite))

	// The configuration moves after the run started.
	DefaultSandbox = &SandboxConfig{ProjectRoot: second, allowedPaths: map[string]bool{}}

	if err := DefaultSandbox.checkPath(runPolicy(ctx), filepath.Join(first, "a.txt")); err != nil {
		t.Fatalf("the root the run started with was refused: %v", err)
	}
	if err := DefaultSandbox.checkPath(runPolicy(ctx), filepath.Join(second, "b.txt")); err == nil {
		t.Fatal("a root introduced after the run started was granted to it; the policy must be frozen")
	}
}

// TestConcurrentPoliciesDoNotSeeEachOther runs two policies against one
// configuration and asserts neither is granted the other's root — the property
// a shared mutable policy cannot offer.
func TestConcurrentPoliciesDoNotSeeEachOther(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()

	box := &SandboxConfig{allowedPaths: map[string]bool{}}
	ctxA := policyCtx(types.SandboxWorkspaceWrite, rootA)
	ctxB := policyCtx(types.SandboxWorkspaceWrite, rootB)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(4)
		go func() {
			defer wg.Done()
			if err := box.checkPath(runPolicy(ctxA), filepath.Join(rootA, "own.txt")); err != nil {
				t.Errorf("A was refused its own root: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := box.checkPath(runPolicy(ctxB), filepath.Join(rootB, "own.txt")); err != nil {
				t.Errorf("B was refused its own root: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if err := box.checkPath(runPolicy(ctxA), filepath.Join(rootB, "other.txt")); err == nil {
				t.Error("A was granted B's root")
			}
		}()
		go func() {
			defer wg.Done()
			if err := box.checkPath(runPolicy(ctxB), filepath.Join(rootA, "other.txt")); err == nil {
				t.Error("B was granted A's root")
			}
		}()
	}
	wg.Wait()
}

// TestPerCallPolicyDoesNotLeakToTheCaller pins the escalation shape: a wider
// policy attached to a derived context governs that call and only it. The
// caller's own value is unchanged, so nothing has to be rolled back.
func TestPerCallPolicyDoesNotLeakToTheCaller(t *testing.T) {
	base := policyCtx(types.SandboxReadOnly, "/project")
	wider := types.WithSandboxPolicy(base, types.SandboxPolicy{
		Mode: types.SandboxWorkspaceWrite, ProjectRoot: "/project", Roots: []string{"/project"},
	})

	if got := runPolicy(wider).Mode; got != types.SandboxWorkspaceWrite {
		t.Fatalf("the derived call ran as %q, want workspace-write", got)
	}
	if got := runPolicy(base).Mode; got != types.SandboxReadOnly {
		t.Fatalf("the caller's policy became %q; a per-call grant must not leak back", got)
	}
}
