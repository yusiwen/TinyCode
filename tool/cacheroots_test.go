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

// TestPlatformCacheRootsUseTheUserCacheDir pins the root every host gets so a
// confined toolchain can write its caches: the platform user cache directory,
// first — PlatformTempDir puts the per-user scratch inside it — followed only by
// existing toolchain caches.
func TestPlatformCacheRootsUseTheUserCacheDir(t *testing.T) {
	want := overrideUserCache(t, t.TempDir())

	roots := PlatformCacheRoots()
	if len(roots) == 0 {
		t.Fatal("PlatformCacheRoots() is empty")
	}
	if roots[0] != want {
		t.Fatalf("PlatformCacheRoots()[0] = %q, want the user cache directory %q (first: TMPDIR lives under it)", roots[0], want)
	}
	for _, root := range roots[1:] {
		if info, err := os.Stat(root); err != nil || !info.IsDir() {
			t.Errorf("PlatformCacheRoots() granted %q, which does not exist: the Linux applier refuses a root it cannot open, so this would break every confined command", root)
		}
	}
}

// installedToolchain makes command appear on PATH for the duration of the test,
// with the user's home redirected so the conventional cache location is the
// test's own temporary directory rather than the developer's.
//
// It is the two facts a toolchain's grant depends on: the command is installed,
// and the cache is where this product expects it.
func installedToolchain(t *testing.T, command string, cachePath ...string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	bin := t.TempDir()
	exe := filepath.Join(bin, command)
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	// A test that asserts a cache for one toolchain must not inherit another's.
	for _, name := range []string{"GOMODCACHE", "GOPATH", "CARGO_HOME", "npm_config_cache", "NPM_CONFIG_CACHE", "XDG_DATA_HOME"} {
		t.Setenv(name, "")
	}
	return filepath.Join(append([]string{home}, cachePath...)...)
}

// TestToolchainCacheIsGrantedOnlyWhenInstalledAndExisting is the cold/warm pair
// from issue #166: an installed toolchain whose cache directory exists is
// granted, and the same toolchain before its cache exists is not — because a
// granted root that cannot be opened would make the Linux applier refuse to run
// the command at all.
func TestToolchainCacheIsGrantedOnlyWhenInstalledAndExisting(t *testing.T) {
	overrideUserCache(t, t.TempDir())
	cache := installedToolchain(t, "go", "go", "pkg", "mod")

	if got := PlatformCacheRoots(); contains(got, cache) {
		t.Errorf("PlatformCacheRoots() granted %q before it existed: %v", cache, got)
	}

	EnsurePlatformCacheRoots()
	if got := PlatformCacheRoots(); !contains(got, cache) {
		t.Errorf("PlatformCacheRoots() = %v, want it to include the installed toolchain's cache %q", got, cache)
	}
	if info, err := os.Stat(cache); err != nil || !info.IsDir() {
		t.Errorf("EnsurePlatformCacheRoots did not create %q: %v %v", cache, info, err)
	}
}

// TestToolchainCacheIsNotGrantedWhenTheToolchainIsAbsent keeps a machine from
// growing caches it never asked for, and keeps the grant honest: a directory
// that merely looks like a toolchain's cache is not evidence that the toolchain
// is installed.
func TestToolchainCacheIsNotGrantedWhenTheToolchainIsAbsent(t *testing.T) {
	overrideUserCache(t, t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // no toolchain on PATH at all
	if err := os.MkdirAll(filepath.Join(home, "go", "pkg", "mod"), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, root := range PlatformCacheRoots() {
		if strings.Contains(root, filepath.Join("go", "pkg", "mod")) {
			t.Fatalf("PlatformCacheRoots() granted %q for a toolchain that is not on PATH", root)
		}
	}
}

// TestToolchainCacheGrantsTheSubtreeNotTheHome is the security rule that makes
// the widening in #166 acceptable: GOPATH holds go/bin beside the module cache
// and CARGO_HOME holds bin beside registry, so granting either home would let a
// confined command leave a program behind that the user later runs — the sandbox
// escaping by persistence.
func TestToolchainCacheGrantsTheSubtreeNotTheHome(t *testing.T) {
	overrideUserCache(t, t.TempDir())

	t.Run("go", func(t *testing.T) {
		moduleCache := installedToolchain(t, "go", "go", "pkg", "mod")
		EnsurePlatformCacheRoots()
		got := PlatformCacheRoots()
		if !contains(got, moduleCache) {
			t.Fatalf("PlatformCacheRoots() = %v, want the module cache %q", got, moduleCache)
		}
		// The two directories a Go install puts on PATH must not be granted. They
		// are checked as paths, not as a substring of the home: granting the home
		// itself is what this rules out.
		gopath := filepath.Dir(filepath.Dir(moduleCache)) // <home>/go
		if contains(got, gopath) {
			t.Errorf("PlatformCacheRoots() granted GOPATH %q, whose bin is on PATH", gopath)
		}
		if contains(got, filepath.Join(gopath, "bin")) {
			t.Errorf("PlatformCacheRoots() granted go's bin directory: %v", got)
		}
	})

	t.Run("cargo", func(t *testing.T) {
		registry := installedToolchain(t, "cargo", ".cargo", "registry")
		EnsurePlatformCacheRoots()
		got := PlatformCacheRoots()
		if !contains(got, registry) {
			t.Fatalf("PlatformCacheRoots() = %v, want the registry cache %q", got, registry)
		}
		cargoHome := filepath.Dir(registry) // <home>/.cargo
		if contains(got, cargoHome) {
			t.Errorf("PlatformCacheRoots() granted CARGO_HOME %q, whose bin is on PATH", cargoHome)
		}
		if contains(got, filepath.Join(cargoHome, "bin")) {
			t.Errorf("PlatformCacheRoots() granted cargo's bin directory: %v", got)
		}
	})
}

// TestToolchainCacheHonoursTheEnvironmentVariables covers the locations a user
// moved deliberately: the cache is where the variable says, and CARGO_HOME's
// registry subtree is still the thing granted.
func TestToolchainCacheHonoursTheEnvironmentVariables(t *testing.T) {
	overrideUserCache(t, t.TempDir())
	installedToolchain(t, "go", "go", "pkg", "mod") // PATH has go

	moduleCache := filepath.Join(t.TempDir(), "gomodcache")
	cargoRegistry := filepath.Join(t.TempDir(), "cargohome", "registry")
	t.Setenv("GOMODCACHE", moduleCache)
	t.Setenv("CARGO_HOME", filepath.Dir(cargoRegistry))
	// cargo has to be installed for its variable to be consulted at all.
	bin := t.TempDir()
	for _, command := range []string{"go", "cargo"} {
		if err := os.WriteFile(filepath.Join(bin, command), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	EnsurePlatformCacheRoots()
	got := PlatformCacheRoots()
	if !contains(got, moduleCache) {
		t.Errorf("PlatformCacheRoots() = %v, want $GOMODCACHE %q", got, moduleCache)
	}
	if !contains(got, cargoRegistry) {
		t.Errorf("PlatformCacheRoots() = %v, want $CARGO_HOME/registry %q", got, cargoRegistry)
	}
	if contains(got, filepath.Dir(cargoRegistry)) {
		t.Errorf("PlatformCacheRoots() granted CARGO_HOME itself: %v", got)
	}
}

// TestToolchainCacheIgnoresRelativeEnvironmentVariables: a relative GOMODCACHE is
// resolved against whatever directory a command runs in, so granting it would
// grant a different path in every project.
func TestToolchainCacheIgnoresRelativeEnvironmentVariables(t *testing.T) {
	overrideUserCache(t, t.TempDir())
	installedToolchain(t, "go", "go", "pkg", "mod")
	t.Setenv("GOMODCACHE", "relative/modcache")
	t.Setenv("CARGO_HOME", "relative/cargohome")

	EnsurePlatformCacheRoots()
	for _, root := range PlatformCacheRoots() {
		if strings.HasPrefix(root, "relative") || strings.Contains(root, "relative/") {
			t.Fatalf("PlatformCacheRoots() granted the relative path %q", root)
		}
	}
}

// contains reports whether the slice holds the exact path.
func contains(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
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
