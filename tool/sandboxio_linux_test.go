//go:build linux

package tool

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestOpenBeneathRootContainment covers the kernel-enforced open on a real
// openat2 kernel: the descriptor the caller gets back can never be outside the
// root, whatever the path looks like.
func TestOpenBeneathRootContainment(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()

	inRoot := filepath.Join(root, "in.txt")
	if err := os.WriteFile(inRoot, []byte("in"), 0644); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	// Symlink escaping through an absolute target, and through a directory link.
	if err := os.Symlink(secret, filepath.Join(root, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape-dir")); err != nil {
		t.Fatal(err)
	}
	// A relative symlink that stays inside the root is legitimate.
	if err := os.Symlink("in.txt", filepath.Join(root, "rel.txt")); err != nil {
		t.Fatal(err)
	}

	read := func(rel string) error {
		f, err := openBeneathRoot(root, rel, os.O_RDONLY, 0)
		if err == nil {
			f.Close()
		}
		return err
	}

	if err := read("in.txt"); err != nil {
		t.Errorf("in-root file: %v", err)
	}
	if err := read("rel.txt"); err != nil {
		t.Errorf("in-root relative symlink: %v", err)
	}
	if err := read("escape.txt"); err == nil {
		t.Error("an absolute symlink pointing outside the root was opened")
	} else if !isEXDEVOrNotFound(err) {
		t.Errorf("escaping symlink error = %v, want EXDEV", err)
	}
	if err := read("escape-dir/secret.txt"); err == nil {
		t.Error("a path through an escaping directory link was opened")
	}
	if err := read("../outside.txt"); err == nil {
		t.Error("a .. escape was opened")
	}

	// Creating a file inside the root works and really writes there.
	f, err := openBeneathRoot(root, "created.txt", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
	if err != nil {
		t.Fatalf("create in-root: %v", err)
	}
	if _, err := f.Write([]byte("created")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "created.txt"))
	if err != nil || string(data) != "created" {
		t.Fatalf("created file = %q, %v", data, err)
	}
}

// isEXDEVOrNotFound reports whether err is the kernel's containment refusal. A
// nonexistent path is accepted as well, since the escape may be detected after
// resolution fails; both outcomes refuse the open.
func isEXDEVOrNotFound(err error) bool {
	return errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOENT)
}
