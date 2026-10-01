//go:build darwin

package tool

import "golang.org/x/sys/unix"

// openDirFlags opens one directory for traversal during the no-follow walk.
//
// macOS has no O_PATH, so the directory is opened read-only: the walk needs read
// permission on every directory it passes through, which is what the Go standard
// library's own os.Root does here. O_NOFOLLOW is the point of the open; see
// openResolvedNoFollow.
const openDirFlags = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
