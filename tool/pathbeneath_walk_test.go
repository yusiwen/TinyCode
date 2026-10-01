//go:build linux || darwin

package tool

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenResolvedNoFollowWalksWithoutFollowingSymlinks pins the mechanism behind
// issue #7's first two windows: the walk opens the resolved path component by
// component, so a symlink standing where a real directory (or the file itself)
// should be is refused instead of being followed out of the sandbox.
func TestOpenResolvedNoFollowWalksWithoutFollowingSymlinks(t *testing.T) {
	// A path whose components are all real directories opens normally. The temp
	// dir is resolved first because that is the contract: the walk is handed the
	// OS-resolved form, which on macOS means /private/var rather than /var.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(sub, "file.txt")
	if err := os.WriteFile(file, []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}

	f, err := openResolvedNoFollow(file, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("openResolvedNoFollow: %v", err)
	}
	data := make([]byte, 5)
	if _, err := f.Read(data); err != nil {
		t.Fatalf("read: %v", err)
	}
	f.Close()
	if string(data) != "hello" {
		t.Errorf("content = %q, want hello", data)
	}

	// Creation works: the last component carries the caller's flags.
	created := filepath.Join(dir, "created.txt")
	f, err = openResolvedNoFollow(created, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0640)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := f.Write([]byte("new")); err != nil {
		t.Fatalf("write: %v", err)
	}
	f.Close()
	if got, err := os.ReadFile(created); err != nil || string(got) != "new" {
		t.Fatalf("created file = %q, %v", got, err)
	}
	if info, err := os.Stat(created); err != nil || info.Mode().Perm() != 0640 {
		t.Errorf("created mode = %v, %v, want 640", info.Mode().Perm(), err)
	}

	// A symlink where a directory component should be: refused, and the target is
	// never opened. This is the swap the check cannot see.
	elsewhere := t.TempDir()
	if err := os.WriteFile(filepath.Join(elsewhere, "file.txt"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, filepath.Join(dir, "swap")); err != nil {
		t.Fatal(err)
	}
	if f, err := openResolvedNoFollow(filepath.Join(dir, "swap", "file.txt"), os.O_RDONLY, 0); err == nil {
		f.Close()
		t.Error("the walk followed a symlinked directory component")
	}

	// A symlink in place of the resolved file itself is refused for the same
	// reason: the resolved form of a path is not a symlink, so one here is a swap.
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if f, err := openResolvedNoFollow(link, os.O_RDONLY, 0); err == nil {
		f.Close()
		t.Error("the walk followed a symlink in place of the resolved file")
	}

	// A path the walk cannot express safely is reported as unsupported so the
	// caller keeps its previous behaviour rather than getting a surprise.
	for _, bad := range []string{"relative.txt", string(filepath.Separator), filepath.Join(dir, "..", "x")} {
		if _, err := openResolvedNoFollow(bad, os.O_RDONLY, 0); err == nil {
			t.Errorf("openResolvedNoFollow(%q) succeeded, want it refused or unsupported", bad)
		}
	}
}
