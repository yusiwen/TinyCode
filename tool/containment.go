package tool

import (
	"errors"
	"sync"
	"sync/atomic"
)

// ContainmentLevel says what actually enforces a file operation's boundary.
type ContainmentLevel string

const (
	// ContainmentKernel means the operating system enforces the boundary as
	// part of reaching the path: a component that leaves the root, or a
	// component swapped for a symlink between the check and the open, is
	// refused by the kernel rather than followed.
	ContainmentKernel ContainmentLevel = "kernel"

	// ContainmentUserspace means only the in-process checks apply —
	// canonicalize, then compare. They answer the question they were written
	// for, but they cannot promise anything about a component swapped after
	// they ran, because no kernel decision is attached to the open.
	ContainmentUserspace ContainmentLevel = "userspace"
)

// The mechanisms behind those levels, named for a report a person reads.
const (
	mechanismOpenat2    = "openat2 RESOLVE_BENEATH"
	mechanismOpenatWalk = "openat walk, O_NOFOLLOW per component"
	mechanismPortable   = "resolved-path checks in process"
)

// Containment is what this host can enforce for a file operation.
type Containment struct {
	Level     ContainmentLevel
	Mechanism string
}

// String renders the pair for a log line or a status report.
func (c Containment) String() string {
	switch {
	case c.Mechanism == "":
		return string(c.Level)
	case c.Level == "":
		return c.Mechanism
	default:
		return string(c.Level) + " (" + c.Mechanism + ")"
	}
}

// probeContainment is the per-platform probe. It is a variable only so this
// package's tests can present a host without a kernel mechanism; production
// never reassigns it.
var probeContainment = defaultProbeContainment

var (
	containmentOnce sync.Once
	containmentInfo Containment
)

// ContainmentInfo reports what confines a file operation on this host, probing
// once per process.
//
// The probe asks the kernel rather than inferring from the platform name, so
// the verdict cannot drift from what the open layer will actually do: a kernel
// that lost openat2, or a build without the walk, reports the weaker level.
func ContainmentInfo() Containment {
	containmentOnce.Do(func() {
		containmentInfo = probeContainment()
	})
	return containmentInfo
}

// resetContainmentForTest installs a probe and clears the once, returning a
// restore function that leaves the real probe to run again on next use.
func resetContainmentForTest(probe func() Containment) func() {
	previous := probeContainment
	probeContainment = probe
	containmentInfo = Containment{}
	containmentOnce = sync.Once{}
	ContainmentInfo()
	return func() {
		probeContainment = previous
		containmentInfo = Containment{}
		containmentOnce = sync.Once{}
	}
}

var requireHardBoundary atomic.Bool

// SetRequireHardBoundary turns the "require a hard boundary" policy on or off.
// It is configuration, not a per-call decision: the host either has a kernel
// mechanism or it does not.
func SetRequireHardBoundary(required bool) { requireHardBoundary.Store(required) }

// HardBoundaryRequired reports whether that policy is on.
func HardBoundaryRequired() bool { return requireHardBoundary.Load() }

var confineCommands atomic.Bool

// SetConfineCommands turns command confinement on or off: when on, every shell
// command runs under the kernel file boundary instead of only under the string
// checks.
//
// It is off by default and is a deliberate choice, not a default: a confined
// command can write only under the session's writable roots, so toolchains that
// write their own caches (GOCACHE, ~/.npm, …) fail unless those roots are
// granted. Flipping it on changes what every command in the session can do.
func SetConfineCommands(confined bool) { confineCommands.Store(confined) }

// ConfineCommands reports whether shell commands run under the boundary.
func ConfineCommands() bool { return confineCommands.Load() }

// CommandConfinementAvailable reports whether this host can confine a
// subprocess at all. It is a different question from ContainmentInfo: a
// platform can enforce the agent's own opens (the macOS component walk) and
// still have no way to confine a command it spawns.
func CommandConfinementAvailable() bool { return commandConfinementAvailable() }

// ErrHardBoundaryUnavailable is returned when the policy demands a kernel
// boundary and the host has none.
//
// It is an error rather than a permission prompt on purpose: no approval can
// create a capability the host does not have, so refusing is the only honest
// answer.
var ErrHardBoundaryUnavailable = errors.New(
	"a hard file boundary is required (sandbox.require_hard_boundary) but this host enforces file operations with in-process checks only; refusing rather than running with a weaker boundary")

// checkHardBoundary applies the policy to one operation.
func checkHardBoundary() error {
	if !HardBoundaryRequired() {
		return nil
	}
	if ContainmentInfo().Level == ContainmentKernel {
		return nil
	}
	return ErrHardBoundaryUnavailable
}

// containmentNote is appended to a file-mutating tool's success result only
// when the kernel is NOT enforcing the boundary, so a degraded host is visible
// in the result the model is reading rather than only in a log line.
//
// It is empty wherever a kernel mechanism exists, which is every supported
// platform with openat2 or the component walk; ordinary results are therefore
// unchanged, and the note cannot become noise that readers learn to skip.
func containmentNote() string {
	if ContainmentInfo().Level == ContainmentKernel {
		return ""
	}
	return " [containment: in-process checks only — this host has no kernel file-boundary mechanism]"
}
