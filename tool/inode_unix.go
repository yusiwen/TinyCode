//go:build unix

package tool

import (
	"io/fs"
	"syscall"
)

// inodeOf identifies a file's inode, so a replacement (a rename over the path)
// counts as a change even when the size and the modification time coincide.
//
// It is a platform file because the type behind fs.FileInfo.Sys() is: *syscall.Stat_t
// on the unix targets this repository builds for, and nothing portable. A platform
// without the field returns 0, which makes the diff fall back to size and mtime
// rather than pretending to know.
func inodeOf(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
