//go:build linux

package tool

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The launcher on Linux confines the command with Landlock: the command, and
// everything it spawns, may write only under the roots the caller allowed. It
// is an unprivileged, self-imposed restriction, so it needs no helper binary
// and no setuid bit.
//
// Only write-class access rights are handled. Reads stay unrestricted, which is
// what the mode vocabulary claims and what keeps the boundary usable: a command
// that cannot read its own inputs is not confined, it is broken.
var landlockWriteRights = []uint64{
	unix.LANDLOCK_ACCESS_FS_WRITE_FILE,
	unix.LANDLOCK_ACCESS_FS_REMOVE_DIR,
	unix.LANDLOCK_ACCESS_FS_REMOVE_FILE,
	unix.LANDLOCK_ACCESS_FS_MAKE_CHAR,
	unix.LANDLOCK_ACCESS_FS_MAKE_DIR,
	unix.LANDLOCK_ACCESS_FS_MAKE_REG,
	unix.LANDLOCK_ACCESS_FS_MAKE_SOCK,
	unix.LANDLOCK_ACCESS_FS_MAKE_FIFO,
	unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK,
	unix.LANDLOCK_ACCESS_FS_MAKE_SYM,
}

// Rights that arrived in later ABIs, with the version that introduced them. An
// older kernel rejects a ruleset that handles a right it does not know, so the
// mask has to be built from the version it reports rather than from the newest
// headers.
var landlockVersionedRights = []struct {
	abi    int
	access uint64
}{
	{2, unix.LANDLOCK_ACCESS_FS_REFER},
	{3, unix.LANDLOCK_ACCESS_FS_TRUNCATE},
}

// landlockPathBeneathAttrSize is sizeof(struct landlock_path_beneath_attr),
// which is __packed: a __u64 followed by a __s32, so 12 bytes and not the 16
// Go would lay out. The kernel compares the size it is given against its own,
// so passing unsafe.Sizeof of the Go struct would be rejected.
const landlockPathBeneathAttrSize = 12

type landlockRulesetAttr struct {
	handledAccessFS uint64
}

type landlockPathBeneathAttr struct {
	allowedAccess uint64
	parentFd      int32
}

// runConfined applies the boundary and replaces this process with the command.
// It only returns when it could not, and then reports the failure itself.
func runConfined(spec launcherSpec) int {
	if err := applyLandlock(spec.mode, spec.roots); err != nil {
		return launcherFail("cannot apply the file boundary: %v", err)
	}
	if err := unix.Exec(spec.argv[0], spec.argv, os.Environ()); err != nil {
		// Past applyLandlock, so the command is not running; saying so is the
		// whole point of the failure status.
		return launcherFail("cannot execute %q: %v", spec.argv[0], err)
	}
	return 0 // unreachable: Exec replaces the process
}

// errLandlockUnavailable reports a host without a usable Landlock.
var errLandlockUnavailable = errors.New("this kernel has no usable Landlock")

// applyLandlock restricts the calling process: write-class access is denied
// everywhere except /dev/null and the given roots.
//
// The restriction is irreversible and inherited by every descendant, which is
// why the launcher is a process of its own.
func applyLandlock(mode string, roots []string) error {
	abi, err := landlockABI()
	if err != nil {
		return err
	}
	handled := landlockHandledRights(abi)
	if handled == 0 {
		return errLandlockUnavailable
	}

	attr := landlockRulesetAttr{handledAccessFS: handled}
	rulesetFd, err := landlockCreateRuleset(uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr))
	if err != nil {
		return fmt.Errorf("create ruleset (ABI %d): %w", abi, err)
	}
	defer unix.Close(rulesetFd)

	// /dev/null stays writable in every mode. Commands redirect to it
	// constantly, and a boundary that breaks `2>/dev/null` is one nobody can
	// keep switched on.
	nullAccess := uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE)
	if handled&unix.LANDLOCK_ACCESS_FS_TRUNCATE != 0 {
		nullAccess |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if err := landlockAllowPath(rulesetFd, "/dev/null", nullAccess); err != nil {
		return fmt.Errorf("allow /dev/null: %w", err)
	}

	// read-only allows no other path; workspace-write allows the caller's
	// roots. A root that cannot be opened is a failure rather than a silent
	// narrowing: running with fewer writable roots than promised would break
	// the command in a way its author cannot see coming.
	if mode == "workspace-write" {
		for _, root := range roots {
			if err := landlockAllowPath(rulesetFd, root, handled); err != nil {
				return fmt.Errorf("allow %q: %w", root, err)
			}
		}
	}

	// Landlock refuses to restrict a process that could still gain privileges.
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFd), 0, 0); errno != 0 {
		return fmt.Errorf("restrict self: %w", errno)
	}
	return nil
}

// landlockABI returns the Landlock ABI version the running kernel implements,
// or an error when it has none.
func landlockABI() (int, error) {
	version, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0,
		uintptr(unix.LANDLOCK_CREATE_RULESET_VERSION))
	if errno != 0 {
		return 0, fmt.Errorf("%w: %v", errLandlockUnavailable, errno)
	}
	return int(version), nil
}

// landlockHandledRights is the write-class mask for one ABI: everything the
// version understands, and nothing it would reject.
func landlockHandledRights(abi int) uint64 {
	var mask uint64
	for _, right := range landlockWriteRights {
		mask |= right
	}
	for _, right := range landlockVersionedRights {
		if abi >= right.abi {
			mask |= right.access
		}
	}
	return mask
}

func landlockCreateRuleset(attr uintptr, size uintptr) (int, error) {
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, attr, size, 0)
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

// landlockAllowPath grants access under path, which may be a file (only that
// file is granted) or a directory (its whole hierarchy is).
func landlockAllowPath(rulesetFd int, path string, access uint64) error {
	// O_PATH opens the target for the kernel's use without needing read
	// permission on it, and without following a trailing symlink.
	pathFd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(pathFd)

	attr := landlockPathBeneathAttr{allowedAccess: access, parentFd: int32(pathFd)}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFd),
		uintptr(unix.LANDLOCK_RULE_PATH_BENEATH),
		uintptr(unsafe.Pointer(&attr)),
		landlockPathBeneathAttrSize,
		0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
