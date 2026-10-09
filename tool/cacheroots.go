package tool

import (
	"os"
	"path/filepath"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// PlatformCacheRoots returns the directories beyond the project that both the
// path fence and the command boundary treat as writable so a confined command
// can run a real toolchain.
//
// It is the platform user cache directory (os.UserCacheDir): GOCACHE, pip's
// wheel cache and most build tools write there, none of it is source, and
// losing it costs a rebuild rather than data. It is one list for both
// consumers, so granting the cache to a confined command also states that the
// agent's own write_file may touch it — the accepted cost of the shared-list
// rule in issue #120.
//
// A host whose user cache directory cannot be determined contributes nothing
// rather than a guessed path: an invented root would widen the fence to
// something nobody chose.
func PlatformCacheRoots() []string {
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		return nil
	}
	return []string{dir}
}

// PlatformTempDir is the scratch directory a confined command should use as
// TMPDIR. It lives under the granted cache root, so the shared platform temp
// area (and with it all of /tmp) is deliberately not a writable root: the
// toolchain is pointed at a per-user scratch inside the boundary instead. The
// go command, for one, creates its build work directory under $TMPDIR, which
// is why a boundary without this breaks `go build`/`go test`.
//
// Returns "" when no cache root exists, so a caller falls back to the real
// environment rather than inventing a path.
func PlatformTempDir() string {
	roots := PlatformCacheRoots()
	if len(roots) == 0 {
		return ""
	}
	return filepath.Join(roots[0], "tinycode", "tmp")
}

// confinedEnv is the environment a confined command runs with. It points
// TMPDIR at the per-user scratch under the granted cache root, because the
// shared temp area is not a writable root. The directory is created here,
// outside the boundary, since the confined command cannot create a directory
// it was never granted; a failure to create it is logged and the command keeps
// the inherited environment instead.
//
// It returns nil for a run that is not workspace-write (no writable root is
// granted, so there is nowhere to point TMPDIR) and when no cache root exists,
// which tells the caller to leave cmd.Env alone.
func confinedEnv(policy types.SandboxPolicy) []string {
	if policy.Mode != types.SandboxWorkspaceWrite {
		return nil
	}
	tmp := PlatformTempDir()
	if tmp == "" {
		return nil
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		tlog.Warn("sandbox", "temp_dir_unavailable", "path", tmp, "err", err.Error())
		return nil
	}
	return append(os.Environ(), "TMPDIR="+tmp)
}
