//go:build !linux

package tool

import "os"

// kernelEscapeCheck is a no-op on platforms without openat2. The portable
// checks in CheckPath (kernel-order symlink resolution plus resolved-path
// containment) still apply.
func kernelEscapeCheck(root, path string) bool { return false }

// openBeneathRoot has no kernel enforcement to offer on this platform, so the
// caller falls back to a plain open after the portable sandbox check.
func openBeneathRoot(rootDir, rel string, flags int, perm os.FileMode) (*os.File, error) {
	return nil, errBeneathUnsupported
}
