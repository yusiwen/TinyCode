package tool

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// overrideUserCache points the platform's user cache directory at dir for the
// duration of the test and returns what os.UserCacheDir() then reports.
//
// It has to be platform-specific, and the difference is not cosmetic:
// os.UserCacheDir() reads XDG_CACHE_HOME on Linux but $HOME/Library/Caches on
// darwin (it does not consult XDG_CACHE_HOME there at all), so a test that sets
// only XDG_CACHE_HOME asserts against the developer's real cache directory on
// macOS — it passes on the Linux CI runner and fails on the Mac the change is
// written on.
func overrideUserCache(t *testing.T, dir string) string {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Setenv("HOME", dir)
	} else {
		t.Setenv("XDG_CACHE_HOME", dir)
	}
	got, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("os.UserCacheDir() with the override: %v", err)
	}
	if !strings.HasPrefix(got, dir) {
		t.Fatalf("os.UserCacheDir() = %q, which is not under the overridden %q", got, dir)
	}
	return got
}

// TestPlatformCacheRootsUseTheUserCacheDir pins the one root the product grants
// so a confined toolchain can write its caches: the platform user cache
// directory, and nothing else.
func TestPlatformCacheRootsUseTheUserCacheDir(t *testing.T) {
	want := overrideUserCache(t, t.TempDir())

	roots := PlatformCacheRoots()
	if len(roots) != 1 {
		t.Fatalf("PlatformCacheRoots() = %v, want exactly the user cache directory", roots)
	}
	if roots[0] != want {
		t.Fatalf("PlatformCacheRoots()[0] = %q, want the user cache directory %q", roots[0], want)
	}
}

// TestConfinedEnvPointsTmpdirUnderTheCacheRoot covers the other half of making
// a confined toolchain work: the shared temp area is not a writable root, so a
// workspace-write command gets a TMPDIR inside the granted cache root, created
// before the command starts.
func TestConfinedEnvPointsTmpdirUnderTheCacheRoot(t *testing.T) {
	overrideUserCache(t, t.TempDir())

	roots := PlatformCacheRoots()
	if len(roots) == 0 {
		t.Fatal("PlatformCacheRoots() is empty, so this test cannot state anything about TMPDIR")
	}
	env := confinedEnv(types.SandboxPolicy{Mode: types.SandboxWorkspaceWrite})
	if env == nil {
		t.Fatal("confinedEnv returned nil for a workspace-write policy")
	}
	tmp := envValue(env, "TMPDIR")
	if tmp == "" {
		t.Fatal("confinedEnv did not set TMPDIR")
	}
	// Both sides are compared in the OS-resolved form, because os.UserCacheDir()
	// reports the spelling the environment gave it (/var/... on macOS) while the
	// policy canonicalizes the same directory to /private/var/... .
	if !beneathRoot(filepath.Clean(resolveRealPath(roots[0])), filepath.Clean(resolveRealPath(tmp))) {
		t.Fatalf("TMPDIR = %q, want it under the granted cache root %q", tmp, roots[0])
	}
	info, err := os.Stat(tmp)
	if err != nil || !info.IsDir() {
		t.Fatalf("TMPDIR %q was not created as a directory: %v", tmp, err)
	}

	// A read-only run grants no root, so there is nowhere to point TMPDIR and
	// the environment is left alone.
	if env := confinedEnv(types.SandboxPolicy{Mode: types.SandboxReadOnly}); env != nil {
		t.Fatalf("confinedEnv( read-only ) = %v, want nil", env)
	}
}

// envValue returns the last value for key in an env slice, or "".
func envValue(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if v, ok := strings.CutPrefix(env[i], key+"="); ok {
			return v
		}
	}
	return ""
}
