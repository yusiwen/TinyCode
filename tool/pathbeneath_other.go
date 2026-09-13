//go:build !linux

package tool

// kernelEscapeCheck is a no-op on platforms without openat2. The portable
// checks in CheckPath (kernel-order symlink resolution plus resolved-path
// containment) still apply.
func kernelEscapeCheck(root, path string) bool { return false }
