package tool

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// errBeneathUnsupported reports that the kernel cannot enforce containment for
// this open (no openat2, an old kernel, or a path that is not beneath the
// root), so the caller falls back to the no-follow walk.
var errBeneathUnsupported = errors.New("kernel path containment unavailable")

// errPathChanged reports that the path no longer resolves to the form the sandbox
// approved: a component was swapped for a symlink (or a symlink was retargeted)
// between the check and the open. The open is refused rather than followed.
var errPathChanged = errors.New("path changed after the sandbox check")

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
// Everything openat2 cannot cover — a path the user explicitly allowed outside
// the root, a platform without the syscall (macOS), a kernel older than 5.6 —
// goes through openResolvedNoFollow, which walks the *resolved* path one
// component at a time with O_NOFOLLOW, and it is only reached after the path is
// confirmed to still resolve to itself (issue #7). Callers must therefore pass
// the resolved form CheckPathAccess returns; a path that resolves differently now
// is refused, because that difference is exactly the swap this layer exists to
// catch.
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

	// The kernel cannot express containment here, so the walk takes over — but
	// only for the path as the sandbox saw it. Re-resolving must be a no-op: if it
	// is not, a component is pointing somewhere else now than it did when
	// CheckPath approved it, and opening the new target would be following the
	// swap.
	if cleaned := filepath.Clean(absoluteNoClean(path)); realPath != cleaned {
		return nil, &os.PathError{Op: "open", Path: path, Err: errPathChanged}
	}
	if file, err := openResolvedNoFollow(realPath, flags, perm); err == nil {
		return file, nil
	} else if !errors.Is(err, errBeneathUnsupported) {
		return nil, err
	}
	return os.OpenFile(path, flags, perm)
}
