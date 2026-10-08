package tool

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWritableRootsCanonicalizesAndDeduplicates covers the property the shared
// helper exists for: two spellings of the same directory must collapse to one
// canonical entry, and blank entries must not become roots.
func TestWritableRootsCanonicalizesAndDeduplicates(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "project-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	sandbox := &SandboxConfig{
		ProjectRoot:    root,
		AutoAllowPaths: []string{link, root, "", "   "},
		allowedPaths:   map[string]bool{},
	}

	roots := sandbox.WritableRoots()
	resolvedRoot := filepath.Clean(resolveRealPath(root))
	if len(roots) != 1 {
		t.Fatalf("WritableRoots() = %v, want exactly one entry: the symlinked duplicate and the blank entries must collapse", roots)
	}
	if roots[0] != resolvedRoot {
		t.Fatalf("WritableRoots()[0] = %q, want the resolved project root %q", roots[0], resolvedRoot)
	}
}

// TestWritableRootsKeepsProjectRootFirst pins the order CheckPath relies on: the
// project root is the entry the kernel probe can express, so it must come first
// and the remaining roots must follow.
func TestWritableRootsKeepsProjectRootFirst(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	other := filepath.Join(base, "other")
	for _, dir := range []string{root, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	sandbox := &SandboxConfig{
		ProjectRoot:    other,
		AutoAllowPaths: []string{root},
		allowedPaths:   map[string]bool{},
	}

	roots := sandbox.WritableRoots()
	if len(roots) != 2 {
		t.Fatalf("WritableRoots() = %v, want two entries", roots)
	}
	if want := filepath.Clean(resolveRealPath(other)); roots[0] != want {
		t.Fatalf("roots[0] = %q, want the project root %q", roots[0], want)
	}
	if want := filepath.Clean(resolveRealPath(root)); roots[1] != want {
		t.Fatalf("roots[1] = %q, want the auto-allowed path %q", roots[1], want)
	}
}

// TestWritableRootsExcludesTempAreasByDefault pins a deliberate policy fact, not
// an accident of the code: the fence does not allow writes to the platform temp
// area, so the shared roots list must not include it. Folding it in here would
// widen the fence as a side effect of a refactor.
func TestWritableRootsExcludesTempAreasByDefault(t *testing.T) {
	root := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}

	sandbox := &SandboxConfig{ProjectRoot: root, allowedPaths: map[string]bool{}}
	temp := filepath.Clean(resolveRealPath(os.TempDir()))
	for _, r := range sandbox.WritableRoots() {
		if r == temp {
			t.Fatalf("WritableRoots() includes the temp area %q; the default policy does not allow it", temp)
		}
	}

	// The list is not the only claim: the fence must refuse it too.
	probe := filepath.Join(os.TempDir(), "tinycode-writable-roots-probe.txt")
	if err := sandbox.CheckPath(probe); err == nil {
		t.Fatalf("CheckPath(%q) was allowed; the default policy does not allow temp-area writes", probe)
	}
}

// TestCheckPathAgreesWithWritableRoots is the agreement test for this step: the
// fence accepts a path exactly when that path lies under a root the shared list
// reports. Command execution will consume the same list, so a divergence here is
// what would let the two drift.
func TestCheckPathAgreesWithWritableRoots(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "project")
	sibling := filepath.Join(base, "sibling")
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{root, sibling, outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	sandbox := &SandboxConfig{
		ProjectRoot:    root,
		AutoAllowPaths: []string{sibling},
		allowedPaths:   map[string]bool{},
	}
	roots := sandbox.WritableRoots()

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"inside the project root", filepath.Join(root, "a.txt"), true},
		{"inside an auto-allowed sibling", filepath.Join(sibling, "b.txt"), true},
		{"outside every root", filepath.Join(outside, "c.txt"), false},
		{"the parent of the project", filepath.Join(base, "d.txt"), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			real := filepath.Clean(resolveRealPath(tc.path))
			inRoots := false
			for _, r := range roots {
				if beneathRoot(r, real) {
					inRoots = true
					break
				}
			}

			err := sandbox.CheckPath(tc.path)
			if inRoots != (err == nil) {
				t.Fatalf("WritableRoots containment = %v but CheckPath allowed = %v (err=%v); the fence and the roots list disagree",
					inRoots, err == nil, err)
			}
			if inRoots != tc.want {
				t.Fatalf("WritableRoots containment = %v, want %v (roots=%v, path=%q)", inRoots, tc.want, roots, tc.path)
			}
		})
	}
}
