package tool

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// withContainment swaps the containment probe for one test and restores the
// real one afterwards, including the require-hard-boundary policy.
func withContainment(t *testing.T, info Containment) {
	t.Helper()
	restore := resetContainmentForTest(func() Containment { return info })
	previousPolicy := HardBoundaryRequired()
	t.Cleanup(func() {
		SetRequireHardBoundary(previousPolicy)
		restore()
	})
}

// TestContainmentProbeRunsOnce pins the contract the startup verdict relies on:
// the probe is a syscall, so it must not run again on every check.
func TestContainmentProbeRunsOnce(t *testing.T) {
	var calls atomic.Int64
	restore := resetContainmentForTest(func() Containment {
		calls.Add(1)
		return Containment{Level: ContainmentKernel, Mechanism: mechanismOpenat2}
	})
	defer restore()

	for i := 0; i < 5; i++ {
		if got := ContainmentInfo().Level; got != ContainmentKernel {
			t.Fatalf("ContainmentInfo().Level = %q, want %q", got, ContainmentKernel)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("probe ran %d times, want exactly 1", got)
	}
}

// TestContainmentNoteOnlyWhenDegraded pins where the fact appears: a host with
// a kernel mechanism adds nothing to a result, so the note cannot become noise
// readers learn to skip, and a degraded host cannot stay invisible either.
func TestContainmentNoteOnlyWhenDegraded(t *testing.T) {
	withContainment(t, Containment{Level: ContainmentKernel, Mechanism: mechanismOpenat2})
	if note := containmentNote(); note != "" {
		t.Fatalf("containmentNote() = %q on a kernel host, want empty", note)
	}

	withContainment(t, Containment{Level: ContainmentUserspace, Mechanism: mechanismPortable})
	note := containmentNote()
	if !strings.Contains(note, "containment") || !strings.Contains(note, "in-process checks only") {
		t.Fatalf("containmentNote() = %q, want it to name the userspace containment", note)
	}
}

// TestWriteResultCarriesTheDegradedFact is the acceptance test for the fact
// being visible in a tool result — not only in a log line. It runs a real
// mutation through the real tool, so it fails if the note is dropped from any
// of the result-formatting call sites that carry it.
func TestWriteResultCarriesTheDegradedFact(t *testing.T) {
	tmpDir := t.TempDir()
	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{ProjectRoot: tmpDir, allowedPaths: make(map[string]bool)}
	defer func() { DefaultSandbox = saved }()

	withContainment(t, Containment{Level: ContainmentUserspace, Mechanism: mechanismPortable})

	target := filepath.Join(tmpDir, "degraded.txt")
	res, err := ApplyPatch().Execute(context.Background(), map[string]any{
		"patch_text": "*** Begin Patch\n*** Add File: " + target + "\n+payload\n*** End Patch\n",
	})
	if err != nil {
		t.Fatalf("apply_patch failed: %v", err)
	}
	if !strings.Contains(res, "in-process checks only") {
		t.Fatalf("result = %q, want it to carry the userspace containment fact", res)
	}
}

// TestHardBoundaryPolicyFailsClosed covers the policy: where a kernel
// mechanism exists the operation proceeds; where it does not, the operation is
// refused instead of silently weakened, and no permission dialog can change
// that — it is not a decision the user is able to grant.
func TestHardBoundaryPolicyFailsClosed(t *testing.T) {
	tmpDir := t.TempDir()

	t.Run("kernel host proceeds", func(t *testing.T) {
		withContainment(t, Containment{Level: ContainmentKernel, Mechanism: mechanismOpenat2})
		SetRequireHardBoundary(true)
		sandbox := &SandboxConfig{ProjectRoot: tmpDir, allowedPaths: map[string]bool{}}
		if err := sandbox.CheckPath(filepath.Join(tmpDir, "a.txt")); err != nil {
			t.Fatalf("CheckPath on a kernel host = %v, want nil", err)
		}
	})

	t.Run("userspace host refuses", func(t *testing.T) {
		withContainment(t, Containment{Level: ContainmentUserspace, Mechanism: mechanismPortable})
		SetRequireHardBoundary(true)
		sandbox := &SandboxConfig{ProjectRoot: tmpDir, allowedPaths: map[string]bool{}}
		err := sandbox.CheckPath(filepath.Join(tmpDir, "b.txt"))
		if !errors.Is(err, ErrHardBoundaryUnavailable) {
			t.Fatalf("CheckPath = %v, want ErrHardBoundaryUnavailable", err)
		}
	})

	t.Run("refuses before the unconfigured escape", func(t *testing.T) {
		withContainment(t, Containment{Level: ContainmentUserspace, Mechanism: mechanismPortable})
		SetRequireHardBoundary(true)
		sandbox := &SandboxConfig{allowedPaths: map[string]bool{}}
		if err := sandbox.CheckPath(filepath.Join(tmpDir, "c.txt")); !errors.Is(err, ErrHardBoundaryUnavailable) {
			t.Fatalf("CheckPath with no project root = %v, want ErrHardBoundaryUnavailable (fail closed)", err)
		}
	})

	t.Run("policy off keeps the weaker boundary usable", func(t *testing.T) {
		withContainment(t, Containment{Level: ContainmentUserspace, Mechanism: mechanismPortable})
		SetRequireHardBoundary(false)
		sandbox := &SandboxConfig{ProjectRoot: tmpDir, allowedPaths: map[string]bool{}}
		if err := sandbox.CheckPath(filepath.Join(tmpDir, "d.txt")); err != nil {
			t.Fatalf("CheckPath with the policy off = %v, want nil", err)
		}
	})
}
