//go:build linux

package tool

import (
	"errors"
	"path/filepath"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

// openat2Unsupported latches once the kernel reports that openat2 is not
// available (Linux < 5.6), so the check is not retried on every call.
var openat2Unsupported atomic.Bool

// kernelEscapeCheck asks the kernel whether path really resolves inside root,
// using openat2(RESOLVE_BENEATH|RESOLVE_NO_MAGICLINKS).
//
// Unlike the lexical and EvalSymlinks checks, the kernel evaluates the path at
// this exact moment and refuses to leave root, which removes the
// check-then-open race for every component. It reports true only when the
// kernel definitively says the path escapes (EXDEV); missing paths, unsupported
// kernels and any other error report false. The layer can therefore only
// tighten the sandbox, never loosen it.
//
// RESOLVE_BENEATH rejects absolute symlinks outright, wherever they point, so a
// caller that gates a safe path on this result must probe the OS-resolved form:
// see the CheckPath call site.
func kernelEscapeCheck(root, path string) bool {
	if openat2Unsupported.Load() {
		return false
	}

	// The remainder keeps ".." components (see relBeneath) so the kernel
	// resolves the path exactly as the real open would.
	rel, ok := relBeneath(root, path)
	if !ok {
		// The root itself, or a path that is not lexically beneath it:
		// CheckPath's own containment test decides.
		return false
	}

	// Open the root itself (following a symlinked root, which is legitimate).
	rootFd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer unix.Close(rootFd)

	how := &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	}

	// A path that is about to be created does not exist yet, so walk up to the
	// deepest component the kernel can resolve.
	candidate := rel
	for {
		fd, openErr := unix.Openat2(rootFd, candidate, how)
		if openErr == nil {
			unix.Close(fd)
			return false
		}

		switch {
		case errors.Is(openErr, unix.EXDEV):
			return true // definitively outside the root
		case errors.Is(openErr, unix.ENOSYS),
			errors.Is(openErr, unix.EINVAL),
			errors.Is(openErr, unix.E2BIG):
			openat2Unsupported.Store(true)
			return false
		case errors.Is(openErr, unix.ENOENT):
			parent := filepath.Dir(candidate)
			if parent == "." || parent == candidate {
				return false
			}
			candidate = parent
		default:
			// ELOOP / ENOTDIR / EACCES and friends: the portable checks already
			// cover these, so do not turn them into a denial here.
			return false
		}
	}
}
