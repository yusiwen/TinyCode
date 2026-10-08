package tool

import (
	"errors"
	"fmt"
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

// writeTempContents writes the payload into the temp file a replacement write
// created. It is a variable so a test can make the write fail and prove the
// target keeps its bytes (issue #36).
var writeTempContents = func(f *os.File, data []byte) error {
	_, err := f.Write(data)
	return err
}

// writeSandboxed replaces a file's contents through the sandbox root, creating it
// with perm when it does not exist yet.
//
// The replacement is atomic: the bytes go into a temp file in the *target's own
// directory* (so the rename stays on one filesystem) and are moved over the target
// with one rename. Opening the target with O_TRUNC and writing into it — what this
// used to do — destroyed the old content before the new bytes were durable, so an
// interrupted write (a signal, a timeout, a full disk) left the file empty or half
// written (issue #36). A reader now sees either the old file or the new one.
//
// The temp file is created through the same sandbox-aware open as the target, so
// it cannot land outside the root, and it is removed on every failure path.
func writeSandboxed(path string, data []byte, perm os.FileMode) error {
	dir, base := filepath.Split(path)
	if base == "" || dir == "" {
		return &os.PathError{Op: "write", Path: path, Err: errors.New("not a file path")}
	}

	// The target's own mode wins when it exists: a replacement must not silently
	// widen or narrow the permissions the file already had (the plain
	// O_TRUNC write this replaces kept them).
	mode := perm
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	tmp, file, err := createTempSandboxed(dir, base, mode)
	if err != nil {
		return err
	}
	// Every failure below must leave the directory as it was.
	committed := false
	defer func() {
		if !committed {
			file.Close()
			_ = os.Remove(tmp)
		}
	}()

	if err := writeTempContents(file, data); err != nil {
		return err
	}
	// Close before the rename: a failed close means the bytes may not have
	// reached the file, and renaming that over the target would be the very data
	// loss this function exists to prevent.
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	committed = true
	return nil
}

// createTempSandboxed creates a temp file next to the target through the
// sandbox-aware open, trying a few names before giving up. It returns the path and
// the open file.
func createTempSandboxed(dir, base string, perm os.FileMode) (string, *os.File, error) {
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		tmp := filepath.Join(dir, fmt.Sprintf(".%s.tinycode-%d-%d.tmp", base, os.Getpid(), attempt))
		file, err := openSandboxed(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			return tmp, file, nil
		}
		lastErr = err
		if !os.IsExist(err) {
			return "", nil, err
		}
	}
	return "", nil, lastErr
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
	// The project root comes from the same derivation the fence uses, so the
	// root this layer hands to the kernel is the root CheckPath approved
	// against. Before, the two computed it with different expressions: this one
	// resolved the configured value directly, while CheckPath cleaned it first.
	// They agreed for every ordinary path and could differ for a root written
	// with ".." or redundant separators — precisely the kind of divergence a
	// second derivation invites.
	realRoot := DefaultSandbox.projectRootResolved()
	if realRoot == "" {
		return os.OpenFile(path, flags, perm)
	}

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
