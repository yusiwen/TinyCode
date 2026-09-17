package tool

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// errBeneathUnsupported reports that the kernel cannot enforce containment for
// this open (no openat2, an old kernel, or a path that is not beneath the
// root), so the caller falls back to a plain open.
var errBeneathUnsupported = errors.New("kernel path containment unavailable")

// readSandboxed reads a file through the sandbox root, so containment is
// enforced by the same open that produces the data.
func readSandboxed(path string) ([]byte, error) {
	f, err := openSandboxed(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// writeSandboxed replaces a file's contents through the sandbox root, creating
// it with perm when it does not exist yet.
func writeSandboxed(path string, data []byte, perm os.FileMode) error {
	f, err := openSandboxed(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// openSandboxed opens a path for I/O so that the kernel decides containment at
// open time, not a check made a moment earlier.
//
// When the path resolves inside the project root the open goes through the root
// descriptor with RESOLVE_BENEATH (see openBeneathRoot), which closes the
// check-then-open window: a directory component swapped for an escaping symlink
// after the permission dialog was answered makes the open fail with EXDEV
// instead of following it.
//
// A path the user explicitly allowed outside the root, and any platform without
// that syscall, falls back to a plain open — the behaviour before this layer,
// where the sandbox check is the only gate.
func openSandboxed(path string, flags int, perm os.FileMode) (*os.File, error) {
	root := DefaultSandbox.ProjectRoot
	if root == "" {
		return os.OpenFile(path, flags, perm)
	}

	realRoot := filepath.Clean(resolveRealPath(absoluteNoClean(root)))
	realPath := filepath.Clean(resolveRealPath(absoluteNoClean(path)))
	if rel, ok := relBeneath(realRoot, realPath); ok {
		file, err := openBeneathRoot(realRoot, rel, flags, perm)
		if err == nil {
			return file, nil
		}
		if !errors.Is(err, errBeneathUnsupported) {
			return nil, err
		}
	}
	return os.OpenFile(path, flags, perm)
}
