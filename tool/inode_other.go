//go:build !unix

package tool

import "io/fs"

// inodeOf has no portable answer on this platform; 0 makes the diff fall back to
// size and modification time, which is what it would compare anyway.
func inodeOf(fs.FileInfo) uint64 { return 0 }
