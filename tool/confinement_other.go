//go:build !linux

package tool

// runConfined has no kernel mechanism to apply on this platform. It reports
// that as a launcher failure rather than running the command unconfined: a
// caller that asked for a boundary and did not get one must be able to tell.
//
// The macOS component walk (pathbeneath_walk.go) confines the *agent's* own
// file operations; it cannot confine a subprocess it does not control, which is
// what this entry point exists for.
func runConfined(_ launcherSpec) int {
	return launcherFail("no kernel file-boundary mechanism for subprocesses on this platform")
}

// commandConfinementAvailable is false here for the same reason: there is no
// mechanism to apply.
func commandConfinementAvailable() bool { return false }
