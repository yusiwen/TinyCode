//go:build linux

package tool

import (
	"errors"

	"golang.org/x/sys/unix"
)

// defaultProbeContainment asks the kernel which mechanism it will actually
// apply to an open, in the same order the open layer tries them.
//
// openat2 with RESOLVE_BENEATH is the strongest: the decision and the open are
// one syscall. Where it is missing, the component walk (openat with
// O_NOFOLLOW on every component) still confines — a symlink swapped in after
// the check is refused instead of followed — so the level stays kernel. Only a
// host with neither falls back to in-process checks.
func defaultProbeContainment() Containment {
	dir, err := unix.Open(".", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		// Nothing to probe against. Report the weaker level rather than
		// claiming a boundary that was not demonstrated.
		return Containment{Level: ContainmentUserspace, Mechanism: mechanismPortable}
	}
	defer unix.Close(dir)

	how := &unix.OpenHow{
		Flags:   unix.O_PATH | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	}
	fd, err := unix.Openat2(dir, ".", how)
	if err == nil {
		unix.Close(fd)
		return Containment{Level: ContainmentKernel, Mechanism: mechanismOpenat2}
	}

	// The same three errno values the escape probe treats as "this kernel does
	// not have openat2"; latch it so the runtime stops trying.
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.E2BIG) {
		openat2Unsupported.Store(true)
		// The walk needs only openat, which predates openat2 by a decade: a
		// kernel that lacks openat2 still confines through it.
		return Containment{Level: ContainmentKernel, Mechanism: mechanismOpenatWalk}
	}

	// Any other error is about this particular path, not about the mechanism.
	// openat2 is present, so the boundary is the kernel's.
	return Containment{Level: ContainmentKernel, Mechanism: mechanismOpenat2}
}
