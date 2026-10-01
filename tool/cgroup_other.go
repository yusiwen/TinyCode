//go:build !linux

package tool

import "errors"

// newBashCgroup has nothing to offer without cgroups: the caller keeps the
// portable process-group and tree-walk kill. It is a variable for the same reason
// as the Linux one: a test can substitute a constructor.
var newBashCgroup = func() (*bashCgroup, error) {
	return nil, errors.New("cgroups are Linux-only")
}
