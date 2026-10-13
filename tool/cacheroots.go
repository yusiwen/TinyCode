package tool

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// PlatformCacheRoots returns the directories beyond the project that both the
// path fence and the command boundary treat as writable so a confined command
// can run a real toolchain: the platform user cache directory, then the download
// cache of every toolchain installed on this host that keeps it elsewhere.
//
// It is one list for both consumers, so granting a cache to a confined command
// also states that the agent's own write_file may touch it — the accepted cost of
// the shared-list rule in issue #120.
//
// Two rules make the widening in issue #166 defensible:
//
//   - **Cache subtrees only, never a directory on PATH.** GOPATH holds ~/go/bin
//     beside ~/go/pkg/mod, and CARGO_HOME holds bin beside registry. A root that
//     included either bin directory would let a confined command leave a program
//     behind that the user later runs outside the boundary — the sandbox escaping
//     by persistence rather than by a write.
//   - **Only a toolchain that is installed and whose cache exists.** Existence is
//     not pedantry: the Linux applier refuses to run with a root it cannot open,
//     because running with fewer roots than promised would break commands in a way
//     that looks like permission denied rather than a boundary. Granting a phantom
//     path would therefore break every confined command on the host.
//     EnsurePlatformCacheRoots creates them for the installed toolchains before
//     this is read, which is what makes a *cold* toolchain work.
//
// A host whose user cache directory cannot be determined contributes nothing
// rather than a guessed path: an invented root would widen the fence to something
// nobody chose.
func PlatformCacheRoots() []string {
	roots := make([]string, 0, 1+len(toolchainCaches()))
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		roots = append(roots, dir)
	}
	for _, tc := range toolchainCaches() {
		if _, err := exec.LookPath(tc.command); err != nil {
			continue // the toolchain is not installed here
		}
		cache := tc.cache()
		if cache == "" {
			continue
		}
		if info, err := os.Stat(cache); err != nil || !info.IsDir() {
			continue // nothing to grant: see EnsurePlatformCacheRoots
		}
		roots = append(roots, cache)
	}
	return dedupeRoots(roots)
}

// EnsurePlatformCacheRoots creates the cache root of every installed toolchain,
// so a cold one can fetch instead of failing on a directory it was never granted
// (issue #166). A confined command cannot create a directory outside its
// boundary, which is the same reason the per-user TMPDIR is created here rather
// than inside the boundary.
//
// Failure is logged, not fatal: a toolchain whose cache cannot be created is
// simply not granted, and the command it runs reports its own permission error —
// which docs/sandbox.md and /sandbox point at the configuration key for.
func EnsurePlatformCacheRoots() {
	for _, tc := range toolchainCaches() {
		if _, err := exec.LookPath(tc.command); err != nil {
			continue
		}
		cache := tc.cache()
		if cache == "" {
			continue
		}
		if err := os.MkdirAll(cache, 0o755); err != nil {
			tlog.Warn("sandbox", "cache_root_unavailable", "path", cache, "err", err.Error())
		}
	}
}

// toolchainCache is one toolchain's download cache: the command whose presence on
// PATH means the toolchain is installed here, and how to name its cache.
type toolchainCache struct {
	command string
	cache   func() string
}

// toolchainCaches are the caches this product knows how to name. Each entry is
// deliberate: the cache of a toolchain whose location is well known and whose
// cache directory holds only downloaded material.
//
// pip is absent on purpose: its cache is $XDG_CACHE_HOME/pip or
// ~/.cache/pip, which is under the platform user cache directory that is granted
// unconditionally, so naming it again would be a second derivation of the same
// path.
func toolchainCaches() []toolchainCache {
	return []toolchainCache{
		{command: "go", cache: goModuleCache},
		{command: "cargo", cache: cargoRegistryCache},
		{command: "npm", cache: npmCache},
		{command: "pnpm", cache: pnpmStoreCache},
	}
}

// goModuleCache is the Go module cache: $GOMODCACHE, else the first $GOPATH
// entry's pkg/mod, else ~/go/pkg/mod.
//
// Only an absolute value is honoured. A relative $GOMODCACHE is resolved against
// whatever directory the command runs in, so granting it would grant something
// else entirely — and `go mod download` inside the project would then write where
// the fence never agreed.
func goModuleCache() string {
	if dir := os.Getenv("GOMODCACHE"); filepath.IsAbs(dir) {
		return filepath.Clean(dir)
	}
	if gopath := os.Getenv("GOPATH"); gopath != "" {
		first := strings.Split(gopath, string(os.PathListSeparator))[0]
		if filepath.IsAbs(first) {
			return filepath.Join(filepath.Clean(first), "pkg", "mod")
		}
	}
	return underHome("go", "pkg", "mod")
}

// cargoRegistryCache is cargo's download cache: $CARGO_HOME/registry, else
// ~/.cargo/registry. CARGO_HOME also holds bin, which is why the subtree is
// appended rather than the home granted.
func cargoRegistryCache() string {
	if home := os.Getenv("CARGO_HOME"); filepath.IsAbs(home) {
		return filepath.Join(filepath.Clean(home), "registry")
	}
	return underHome(".cargo", "registry")
}

// npmCache is npm's cache: $npm_config_cache, else ~/.npm. The cache directory
// holds _cacache and logs; npm's global bin directory is configured elsewhere
// (npm prefix), so this cannot grant a program the user would run.
func npmCache() string {
	for _, name := range []string{"npm_config_cache", "NPM_CONFIG_CACHE"} {
		if dir := os.Getenv(name); filepath.IsAbs(dir) {
			return filepath.Clean(dir)
		}
	}
	return underHome(".npm")
}

// pnpmStoreCache is pnpm's content-addressable store: the store subtree of
// pnpm's home, which is ~/Library/pnpm on macOS and $XDG_DATA_HOME/pnpm (or
// ~/.local/share/pnpm) elsewhere.
//
// $PNPM_HOME is deliberately not consulted: it names the directory whose bin holds
// the pnpm executable itself, which is exactly the kind of path this must not
// grant.
func pnpmStoreCache() string {
	if runtime.GOOS == "darwin" {
		return underHome("Library", "pnpm", "store")
	}
	if data := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(data) {
		return filepath.Join(filepath.Clean(data), "pnpm", "store")
	}
	return underHome(".local", "share", "pnpm", "store")
}

// underHome joins elements under the user's home directory, or "" when the home
// directory cannot be determined — an invented location would widen the fence to
// something nobody chose.
func underHome(elements ...string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(append([]string{home}, elements...)...)
}

// dedupeRoots drops blanks and repeats while keeping the first occurrence's
// order: the platform user cache directory must stay first, because
// PlatformTempDir puts the per-user scratch inside it.
func dedupeRoots(candidates []string) []string {
	roots := make([]string, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		roots = append(roots, candidate)
	}
	return roots
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
