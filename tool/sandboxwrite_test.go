package tool

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteSandboxedKeepsTheTargetOnFailure is the gate for issue #36: the write
// goes into a temp file and is renamed over the target, so a failure anywhere
// before the rename leaves the original bytes untouched. The seam makes the write
// itself fail, which is the interruption (a signal, a timeout, a full disk) the
// old O_TRUNC-and-write could not survive.
func TestWriteSandboxedKeepsTheTargetOnFailure(t *testing.T) {
	previous := writeTempContents
	writeTempContents = func(f *os.File, data []byte) error {
		// Write half of it, then fail: a partial write is exactly what must never
		// become visible at the target path.
		if _, err := f.Write(data[:len(data)/2]); err != nil {
			return err
		}
		return errors.New("injected write failure")
	}
	t.Cleanup(func() { writeTempContents = previous })

	dir := t.TempDir()
	target := filepath.Join(dir, "keep.txt")
	if err := os.WriteFile(target, []byte("original bytes\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err := writeSandboxed(target, []byte("replacement bytes that must not land\n"), 0644)
	if err == nil {
		t.Fatal("the injected failure must surface")
	}
	if !strings.Contains(err.Error(), "injected write failure") {
		t.Errorf("error = %v, want the injected failure", err)
	}

	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("read the target: %v", readErr)
	}
	if string(got) != "original bytes\n" {
		t.Errorf("target holds %q, want the original bytes", got)
	}

	// No temp file may be left next to it, and the target must be the only entry.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "keep.txt" {
			t.Errorf("a stray file survived the failed write: %s", e.Name())
		}
	}
}

// TestWriteSandboxedReplacesAtomically covers the successful path: the content is
// replaced, the file keeps the mode it already had, and no temp file is left
// behind.
func TestWriteSandboxedReplacesAtomically(t *testing.T) {
	previousRoot := DefaultSandbox.ProjectRoot
	DefaultSandbox.ProjectRoot = t.TempDir()
	t.Cleanup(func() { DefaultSandbox.ProjectRoot = previousRoot })

	// The wrapper takes the resolved form the sandbox gate hands it (issue #7);
	// on macOS the temp dir is under /var, a symlink to /private/var.
	dir := DefaultSandbox.ResolvePath(t.TempDir())
	target := filepath.Join(dir, "mode.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := writeSandboxed(target, []byte("after\n"), 0644); err != nil {
		t.Fatalf("writeSandboxed: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read the target: %v", err)
	}
	if string(got) != "after\n" {
		t.Errorf("target holds %q, want the replacement", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("mode = %o, want the existing 600 to survive the replacement", mode)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "mode.txt" {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory holds %v, want only the target", names)
	}
}

// TestWriteSandboxedCreatesOutsideTheRootToo keeps the out-of-root path honest
// through the change: the temp file is created through the same sandbox-aware open
// as the target, so an allowed path outside the project root still works and no
// temp file escapes.
func TestWriteSandboxedCreatesOutsideTheRootToo(t *testing.T) {
	previousRoot := DefaultSandbox.ProjectRoot
	DefaultSandbox.ProjectRoot = t.TempDir()
	t.Cleanup(func() { DefaultSandbox.ProjectRoot = previousRoot })

	outside := DefaultSandbox.ResolvePath(filepath.Join(t.TempDir(), "outside.txt"))
	if err := writeSandboxed(outside, []byte("outside\n"), 0644); err != nil {
		t.Fatalf("writeSandboxed outside the root: %v", err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "outside\n" {
		t.Fatalf("outside file = %q, %v", got, err)
	}
	if entries, err := os.ReadDir(filepath.Dir(outside)); err != nil || len(entries) != 1 {
		t.Errorf("directory holds %v (err %v), want only the target", entries, err)
	}
}
