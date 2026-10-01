//go:build !linux && !darwin

package tool

import "os"

// openResolvedNoFollow has no component walk on this platform: there is no
// openat, so the caller falls back to opening by name, exactly as it did before
// the walk existed. The portable sandbox checks in CheckPath are the only gate
// there.
func openResolvedNoFollow(path string, flags int, perm os.FileMode) (*os.File, error) {
	return nil, errBeneathUnsupported
}
