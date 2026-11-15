package agent

import (
	"errors"
	"io/fs"
	"os"
	"runtime"
	"time"
)

// replaceFile renames src over dst, at the end of an atomic rewrite.
//
// On Unix that is one rename. On Windows it can fail with "Access is denied"
// even when the caller holds no handle: an indexer, a virus scanner or another
// reader with the destination open refuses the replace for a moment. The rename
// is the last step of a successful remember, so a short retry is worth more than
// returning a failure the person would read as "memory is broken".
//
// The wait is bounded (about a second) and only happens where the platform
// actually refuses; a Unix rename over an open file succeeds and never enters
// the loop.
func replaceFile(src, dst string) error {
	err := os.Rename(src, dst)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	for i := 0; i < 20 && errors.Is(err, fs.ErrPermission); i++ {
		time.Sleep(50 * time.Millisecond)
		err = os.Rename(src, dst)
	}
	if err == nil || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	// A destination another process holds open for its whole lifetime refuses the
	// replace however long the retry waits — a scanner quarantine, an editor, a
	// second store in the same process that has not closed yet. Removing the old
	// file and renaming the new one into place is what "replace" means there; the
	// temp file is complete and fsynced before this point, so the window in which
	// neither name exists is one rename wide.
	if rmErr := os.Remove(dst); rmErr == nil || errors.Is(rmErr, fs.ErrNotExist) {
		return os.Rename(src, dst)
	}
	return err
}
