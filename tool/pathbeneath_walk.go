//go:build linux || darwin

package tool

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openResolvedNoFollow opens an absolute, already-resolved path one component at
// a time, every component with O_NOFOLLOW, and returns the descriptor the caller
// reads or writes.
//
// This is the fallback for every path openat2 cannot cover (issue #7): a path the
// user allowed outside the project root has no common dirfd to be RESOLVE_BENEATH
// against, and platforms without openat2 (macOS) have no single-syscall form at
// all. Opening such a path by name after the sandbox decision is the
// check-then-open window: a component swapped for an escaping symlink in between
// would be followed. The walk closes that window one component at a time, so a
// host that reaches here is still kernel-enforced — what it lacks is the atomic
// decision-and-open that RESOLVE_BENEATH performs, not the boundary.
//
// A resolved path contains no symlink by construction, so any symlink found here
// was swapped in after the check and is refused with ELOOP instead of followed.
// The walk therefore never needs to decide whether a symlink is "allowed": it
// cannot legitimately see one.
func openResolvedNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	clean := filepath.Clean(path)
	sep := string(filepath.Separator)
	if !filepath.IsAbs(clean) || clean == sep {
		return nil, errBeneathUnsupported
	}

	// Reject anything the walk cannot express; the resolved form has no empty,
	// "." or ".." components, so this is a guard against a caller that did not
	// resolve the path rather than a case the walk has to handle.
	parts := strings.Split(strings.TrimPrefix(clean, sep), sep)
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, errBeneathUnsupported
		}
	}

	dirFd, err := unix.Open(sep, openDirFlags, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: sep, Err: err}
	}

	// Walk the directory part, closing each handle as the next one is opened.
	for _, part := range parts[:len(parts)-1] {
		next, err := unix.Openat(dirFd, part, openDirFlags, 0)
		unix.Close(dirFd)
		if err != nil {
			return nil, &os.PathError{Op: "openat", Path: clean, Err: err}
		}
		dirFd = next
	}
	defer unix.Close(dirFd)

	// The last component carries the caller's flags (O_CREAT and friends); the
	// no-follow flag is ours and is added on top.
	fd, err := unix.Openat(dirFd, parts[len(parts)-1],
		flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(perm.Perm()))
	if err != nil {
		return nil, &os.PathError{Op: "openat", Path: clean, Err: err}
	}
	return os.NewFile(uintptr(fd), clean), nil
}
