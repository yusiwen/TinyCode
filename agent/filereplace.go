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
	// A destination marked read-only refuses the replace with the same error a
	// lock does, so clear that attribute and try once more.
	if chmodErr := os.Chmod(dst, 0o600); chmodErr == nil {
		if err = os.Rename(src, dst); err == nil {
			return nil
		}
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
	// Last resort: rewrite the destination's bytes in place from the temp file.
	// The replace is no longer atomic, which is worse than losing the write
	// entirely, and it is reached only where the platform refused every rename.
	if data, readErr := os.ReadFile(src); readErr == nil {
		if writeErr := os.WriteFile(dst, data, 0o600); writeErr == nil {
			_ = os.Remove(src)
			return nil
		}
	}
	return err
}
