package tool

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSandboxedReadWrite covers the I/O wrappers: they must read, create and
// truncate exactly like the plain file calls they replaced, whether the path is
// inside the sandbox root, outside it (which the permission layer allows case by
// case) or the sandbox is not configured at all.
func TestSandboxedReadWrite(t *testing.T) {
	root := t.TempDir()
	previousRoot := DefaultSandbox.ProjectRoot
	DefaultSandbox.ProjectRoot = root
	t.Cleanup(func() { DefaultSandbox.ProjectRoot = previousRoot })

	inside := filepath.Join(root, "sub", "file.txt")
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
	if _, err := readSandboxed(filepath.Join(root, "missing.txt")); err == nil {
		t.Error("reading a missing file must fail")
	}

	// A symlink inside the root that points inside the root still works: the
	// wrapper resolves the path the same way the permission layer does.
	link := filepath.Join(root, "link.txt")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if data, err := readSandboxed(link); err != nil || string(data) != "x" {
		t.Fatalf("in-root symlink read = %q, %v", data, err)
	}

	// A path outside the root is opened plainly: the sandbox decision belongs to
	// the permission layer, this wrapper only adds kernel enforcement in-root.
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := writeSandboxed(outside, []byte("out"), 0644); err != nil {
		t.Fatalf("writeSandboxed outside: %v", err)
	}
	if data, err := readSandboxed(outside); err != nil || string(data) != "out" {
		t.Fatalf("readSandboxed outside = %q, %v", data, err)
	}

	// Without a project root every path is a plain open.
	DefaultSandbox.ProjectRoot = ""
	if data, err := readSandboxed(outside); err != nil || string(data) != "out" {
		t.Fatalf("plain read = %q, %v", data, err)
	}
}
