//go:build linux

package tool

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// Environment of the child half below.
const (
	landlockChildEnv = "TINYCODE_LANDLOCK_CHILD"
	landlockRootsEnv = "TINYCODE_LANDLOCK_ROOTS"
	landlockInnerEnv = "TINYCODE_LANDLOCK_INSIDE"
	landlockOuterEnv = "TINYCODE_LANDLOCK_OUTSIDE"
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
