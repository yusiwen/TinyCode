//go:build linux

package tool

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yusiwen/tinycode/types"
)

// Environment of the child half below.
const (
	landlockChildEnv = "TINYCODE_LANDLOCK_CHILD"
	landlockRootsEnv = "TINYCODE_LANDLOCK_ROOTS"
	landlockInnerEnv = "TINYCODE_LANDLOCK_INSIDE"
	landlockOuterEnv = "TINYCODE_LANDLOCK_OUTSIDE"
)

// Environment of the exec child used by TestRepoBuildAndTestRunUnderTheDefaultBoundary.
const (
	landlockExecEnv      = "TINYCODE_LANDLOCK_EXEC"
	landlockExecRootsEnv = "TINYCODE_LANDLOCK_EXEC_ROOTS"
	landlockExecArgvEnv  = "TINYCODE_LANDLOCK_EXEC_ARGV"
)

// TestLandlockChildHelper is not a test: it is the child half of
// TestLandlockBoundaryIsEnforced, run through -test.run so that the boundary is
// applied in a process of its own. Landlock restriction is irreversible and
// inherited, so it cannot be applied inside the parent's own process image.
//
// Exit codes name the stage, which is what makes a failure readable:
// 11 apply, 12 inside refused, 13 outside written, 14 outside failed for a
// reason other than a permission denial.
func TestLandlockChildHelper(t *testing.T) {
	if os.Getenv(landlockChildEnv) != "1" {
		t.Skip("child half of TestLandlockBoundaryIsEnforced")
	}

	if err := applyLandlock("workspace-write", []string{os.Getenv(landlockRootsEnv)}); err != nil {
		fmt.Fprintf(os.Stderr, "apply landlock: %v\n", err)
		os.Exit(11)
	}

	if err := os.WriteFile(os.Getenv(landlockInnerEnv), []byte("inside\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "inside write refused: %v\n", err)
		os.Exit(12)
	}

	err := os.WriteFile(os.Getenv(landlockOuterEnv), []byte("outside\n"), 0o644)
	switch {
	case err == nil:
		fmt.Fprintln(os.Stderr, "outside write succeeded under the boundary")
		os.Exit(13)
	case !errors.Is(err, unix.EACCES) && !errors.Is(err, os.ErrPermission):
		fmt.Fprintf(os.Stderr, "outside write failed for another reason: %v\n", err)
		os.Exit(14)
	}
	os.Exit(0)
}

// TestLandlockBoundaryIsEnforced is the acceptance test for the mechanism: a
// process restricted to one root can write inside it and cannot write outside
// it. It is skipped where the kernel has no Landlock rather than failing, so a
// host without the feature is reported as a skip and not as a broken boundary.
func TestLandlockBoundaryIsEnforced(t *testing.T) {
	if _, err := landlockABI(); err != nil {
		t.Skipf("no Landlock on this kernel: %v", err)
	}

	base := t.TempDir()
	root := filepath.Join(base, "writable")
	elsewhere := filepath.Join(base, "not-granted")
	for _, dir := range []string{root, elsewhere} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	inside := filepath.Join(root, "inside.txt")
	outside := filepath.Join(elsewhere, "outside.txt")

	cmd := exec.Command(os.Args[0], "-test.run=TestLandlockChildHelper")
	cmd.Env = append(os.Environ(),
		landlockChildEnv+"=1",
		landlockRootsEnv+"="+root,
		landlockInnerEnv+"="+inside,
		landlockOuterEnv+"="+outside,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}

	// Assert the effects from here as well, so a child that exits 0 without
	// doing the work cannot pass silently.
	if _, err := os.Stat(inside); err != nil {
		t.Fatalf("inside file was not written: %v", err)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("outside file was written: the boundary did not hold")
	}
}

// TestLandlockExecHelper is the child half of
// TestRepoBuildAndTestRunUnderTheDefaultBoundary: it applies the boundary to itself
// and then execs the command, so the parent asserts the real command's exit
// status under the real boundary. Landlock cannot be applied inside the
// parent's own process image, which is why this is a child.
func TestLandlockExecHelper(t *testing.T) {
	if os.Getenv(landlockExecEnv) != "1" {
		t.Skip("child half of TestRepoBuildAndTestRunUnderTheDefaultBoundary")
	}

	var roots []string
	if raw := os.Getenv(landlockExecRootsEnv); raw != "" {
		roots = strings.Split(raw, "\n")
	}
	if err := applyLandlock("workspace-write", roots); err != nil {
		fmt.Fprintf(os.Stderr, "apply landlock: %v\n", err)
		os.Exit(11)
	}

	argv := strings.Split(os.Getenv(landlockExecArgvEnv), "\n")
	if len(argv) == 0 || argv[0] == "" {
		fmt.Fprintln(os.Stderr, "no command to exec")
		os.Exit(15)
	}
	binary, err := exec.LookPath(argv[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot find %q: %v\n", argv[0], err)
		os.Exit(16)
	}
	if err := unix.Exec(binary, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "cannot execute %q: %v\n", binary, err)
		os.Exit(17)
	}
}

// TestRepoBuildAndTestRunUnderTheDefaultBoundary is the acceptance test for
// issue #139 on a host with a mechanism: the repository's own build and test
// commands run under the boundary the product derives by default, with the
// platform user cache root granted and TMPDIR pointed inside it. It is skipped
// only where the kernel has no Landlock.
func TestRepoBuildAndTestRunUnderTheDefaultBoundary(t *testing.T) {
	if _, err := landlockABI(); err != nil {
		t.Skipf("no Landlock on this kernel: %v", err)
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skipf("no go on PATH: %v", err)
	}

	cacheRoots := PlatformCacheRoots()
	if len(cacheRoots) == 0 {
		t.Skip("no platform user cache directory to grant")
	}
	repoRoot, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "go.mod")); err != nil {
		t.Skipf("not running from the repository's tool/ directory: %v", err)
	}

	// The roots are the product's, not this test's: PolicyFromConfig is the one
	// derivation the fence and the command boundary both consume, so the
	// boundary asserted here is the one the default actually produces rather
	// than a list assembled in the test.
	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{
		ProjectRoot:  repoRoot,
		CacheRoots:   cacheRoots,
		allowedPaths: map[string]bool{},
	}
	defer func() { DefaultSandbox = saved }()
	roots := PolicyFromConfig(DefaultSandbox, types.SandboxWorkspaceWrite).Roots

	tmp := PlatformTempDir()
	if tmp == "" {
		t.Skip("no platform temp dir under the cache root")
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}

	// GOCACHE is pointed inside the granted cache root instead of skipping when
	// the ambient one lies elsewhere: a redirected cache is exactly the
	// environment this repository documents for local verification, and
	// skipping there would leave the acceptance assertion unrun precisely where
	// it is most likely to be relied on.
	gocache := filepath.Join(cacheRoots[0], "go-build")

	runUnderBoundary(t, goBin, roots, tmp, gocache, "build", "./...")
	runUnderBoundary(t, goBin, roots, tmp, gocache, "test", "./config/", "./types/", "-count=1")
}

// runUnderBoundary runs one go subcommand through the launcher child with the
// given roots, TMPDIR and build cache, and fails the test if the command does
// not exit 0.
func runUnderBoundary(t *testing.T, goBin string, roots []string, tmp, gocache string, args ...string) {
	t.Helper()
	argv := append([]string{goBin}, args...)
	cmd := exec.Command(os.Args[0], "-test.run=TestLandlockExecHelper")
	cmd.Dir = repoRootForTest(t)
	cmd.Env = append(os.Environ(),
		landlockExecEnv+"=1",
		landlockExecRootsEnv+"="+strings.Join(roots, "\n"),
		landlockExecArgvEnv+"="+strings.Join(argv, "\n"),
		"TMPDIR="+tmp,
		"GOCACHE="+gocache,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("`go %s` failed under the default boundary: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// repoRootForTest returns the repository root the test is running from.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// TestLandlockHandledRightsFollowTheABI pins the version rule: handling a right
// the running kernel does not know makes the ruleset creation fail outright, so
// the mask has to be trimmed to the reported ABI.
func TestLandlockHandledRightsFollowTheABI(t *testing.T) {
	abi1 := landlockHandledRights(1)
	if abi1&unix.LANDLOCK_ACCESS_FS_REFER != 0 {
		t.Error("ABI 1 mask handles REFER, which ABI 1 does not have")
	}
	if abi1&unix.LANDLOCK_ACCESS_FS_TRUNCATE != 0 {
		t.Error("ABI 1 mask handles TRUNCATE, which ABI 1 does not have")
	}
	if abi1&unix.LANDLOCK_ACCESS_FS_WRITE_FILE == 0 {
		t.Error("ABI 1 mask does not handle WRITE_FILE, which is the point")
	}

	abi3 := landlockHandledRights(3)
	if abi3&unix.LANDLOCK_ACCESS_FS_REFER == 0 || abi3&unix.LANDLOCK_ACCESS_FS_TRUNCATE == 0 {
		t.Error("ABI 3 mask must handle REFER and TRUNCATE")
	}
	if abi3&unix.LANDLOCK_ACCESS_FS_IOCTL_DEV != 0 {
		t.Error("IOCTL_DEV is deliberately not handled: it governs ioctl on devices, not file writes")
	}
}
