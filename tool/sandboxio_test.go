package tool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestSandboxedReadWrite covers the I/O wrappers: they must read, create and
// truncate exactly like the plain file calls they replaced, whether the path is
// inside the sandbox root, outside it (which the permission layer allows case by
// case) or the sandbox is not configured at all.
//
// Every path goes through DefaultSandbox.ResolvePath, which is the form
// CheckPathAccess hands the tools: the wrappers require the OS-resolved path so
// that one whose resolution changed since the check can be refused (issue #7).
// The macOS temp dir is a symlinked ancestor (/var → /private/var), so this also
// pins that the resolved-form requirement does not break ordinary paths.
func TestSandboxedReadWrite(t *testing.T) {
	root := t.TempDir()
	previousRoot := DefaultSandbox.ProjectRoot
	DefaultSandbox.ProjectRoot = root
	t.Cleanup(func() { DefaultSandbox.ProjectRoot = previousRoot })

	resolve := DefaultSandbox.ResolvePath

	inside := resolve(filepath.Join(root, "sub", "file.txt"))
	if err := os.MkdirAll(filepath.Dir(inside), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if err := writeSandboxed(inside, []byte("hello"), 0644); err != nil {
		t.Fatalf("writeSandboxed: %v", err)
	}
	got, err := readSandboxed(inside)
	if err != nil || string(got) != "hello" {
		t.Fatalf("readSandboxed = %q, %v", got, err)
	}
	info, err := os.Stat(inside)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0644 {
		t.Errorf("created file mode = %o, want 644", perm)
	}

	// Writing replaces the content instead of appending to it.
	if err := writeSandboxed(inside, []byte("x"), 0644); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got, _ := readSandboxed(inside); string(got) != "x" {
		t.Fatalf("content after overwrite = %q, want x", got)
	}

	// A missing file reports the error, as os.ReadFile did.
	if _, err := readSandboxed(resolve(filepath.Join(root, "missing.txt"))); err == nil {
		t.Error("reading a missing file must fail")
	}

	// A symlink inside the root that points inside the root still works: the
	// permission layer resolves it, so the wrapper is handed the target.
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if data, err := readSandboxed(resolve(link)); err != nil || string(data) != "x" {
		t.Fatalf("in-root symlink read = %q, %v", data, err)
	}

	// A path outside the root is opened through the no-follow walk: the sandbox
	// decision belongs to the permission layer, this wrapper adds the containment
	// it can (issue #7).
	outside := resolve(filepath.Join(t.TempDir(), "outside.txt"))
	if err := writeSandboxed(outside, []byte("out"), 0644); err != nil {
		t.Fatalf("writeSandboxed outside: %v", err)
	}
	if data, err := readSandboxed(outside); err != nil || string(data) != "out" {
		t.Fatalf("readSandboxed outside = %q, %v", data, err)
	}

	// Creating a file that does not exist yet works through the walk too.
	created := resolve(filepath.Join(t.TempDir(), "created.txt"))
	if err := writeSandboxed(created, []byte("new"), 0644); err != nil {
		t.Fatalf("writeSandboxed create: %v", err)
	}
	if data, err := readSandboxed(created); err != nil || string(data) != "new" {
		t.Fatalf("created file = %q, %v", data, err)
	}

	// Without a project root every path is a plain open.
	DefaultSandbox.ProjectRoot = ""
	if data, err := readSandboxed(outside); err != nil || string(data) != "out" {
		t.Fatalf("plain read = %q, %v", data, err)
	}
}

// TestSandboxedOpenRefusesAStaleResolution is the gate for the check-then-open
// window issue #7 keeps open for out-of-root authorizations: the permission layer
// approves a *resolved* path, and if a component of it is a symlink somewhere else
// by the time the file is opened, the open must be refused rather than follow the
// swap.
func TestSandboxedOpenRefusesAStaleResolution(t *testing.T) {
	root := t.TempDir()
	previousRoot := DefaultSandbox.ProjectRoot
	DefaultSandbox.ProjectRoot = root
	t.Cleanup(func() { DefaultSandbox.ProjectRoot = previousRoot })

	base := t.TempDir()
	approved := DefaultSandbox.ResolvePath(filepath.Join(base, "real", "sub", "notes.md"))
	if err := os.MkdirAll(filepath.Dir(approved), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approved, []byte("approved"), 0644); err != nil {
		t.Fatal(err)
	}
	if data, err := readSandboxed(approved); err != nil || string(data) != "approved" {
		t.Fatalf("readSandboxed of the approved path = %q, %v", data, err)
	}

	// Swap a directory component of that exact path for a symlink to a different
	// directory holding a file of the same name: the name still resolves, but not
	// to the file the sandbox approved.
	swapped := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(swapped, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(swapped, "notes.md"), []byte("attacker"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(base, "real", "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(swapped, filepath.Join(base, "real", "sub")); err != nil {
		t.Fatal(err)
	}

	data, err := readSandboxed(approved)
	if err == nil {
		t.Fatalf("readSandboxed followed the swapped component and returned %q", data)
	}
	if !errors.Is(err, errPathChanged) {
		t.Errorf("error = %v, want it to wrap errPathChanged", err)
	}
}
