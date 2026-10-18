//go:build darwin

package tool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// The launcher on macOS confines the command with Seatbelt, applied by the
// system's own sandbox-exec. Unlike Landlock it is not a restriction a process
// can apply to itself, so the boundary is installed through an intermediate
// exec: this launcher becomes sandbox-exec, which becomes the command.
//
// The scope deliberately matches the Linux applier: write-class file access
// only. Reads, the network and process creation stay unrestricted, because a
// boundary that stops `go mod download` from opening a socket is not a file
// boundary, and the two platforms have to mean the same thing by confinement.
//
// sandbox-exec is deprecated by Apple. That is the reason everything here fails
// closed: a launcher that cannot apply the boundary refuses the command rather
// than running it unconfined, so the day the binary disappears the posture
// degrades to "commands are refused" instead of silently "commands are free".
const (
	// sandboxExecPath is Apple's Seatbelt applier. Fixed rather than discovered
	// through PATH: the boundary has to be applied by the system's own copy, and
	// a PATH entry is one an earlier command could have written.
	sandboxExecPath = "/usr/bin/sandbox-exec"

	// seatbeltAllowAll is the profile the capability probe runs under: it proves
	// sandbox-exec applies *a* profile and says nothing about the boundary
	// itself, which the acceptance test covers.
	seatbeltAllowAll = "(version 1)(allow default)"
)

// seatbeltProfile renders the profile for one mode.
//
// The shape is inverted relative to Landlock's ruleset. Landlock starts by
// denying everything the ruleset handles and then grants paths; a Seatbelt
// profile starts from (allow default) and must deny the class first and re-allow
// the roots afterwards. Reversing that order yields a profile that reads as
// restrictive and denies nothing.
//
// Every root is resolved before it is written, because Seatbelt matches the path
// the kernel resolved rather than the one the command typed: on macOS /tmp, /var
// and /etc are symlinks into /private, so a rule for /tmp/x matches nothing while
// the command can still address the file as /tmp/x. A root that cannot be
// resolved is an error rather than a silently narrower boundary — the same rule
// the Linux applier follows when it cannot open a root.
func seatbeltProfile(mode string, roots []string) (string, error) {
	var b strings.Builder
	b.WriteString("(version 1)\n")
	b.WriteString("(allow default)\n")
	b.WriteString("(deny file-write*)\n")

	// /dev/null stays writable in every mode, for the reason the Linux applier
	// grants it: `2>/dev/null` is in every second command, and a boundary that
	// breaks it is one nobody can keep switched on. Note that this is a real
	// loss on macOS and not a formality — a plain `> /dev/null` fails under
	// (deny file-write*) without it.
	b.WriteString("(allow file-write* (literal " + seatbeltQuote("/dev/null") + "))\n")

	if mode == "read-only" {
		return b.String(), nil
	}
	for _, root := range roots {
		resolved, err := resolveSeatbeltRoot(root)
		if err != nil {
			return "", err
		}
		b.WriteString("(allow file-write* (subpath " + seatbeltQuote(resolved) + "))\n")
	}
	return b.String(), nil
}

// resolveSeatbeltRoot turns one caller-supplied root into the path Seatbelt will
// match.
func resolveSeatbeltRoot(root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("empty writable root")
	}
	for _, r := range root {
		if r < 0x20 || r == 0x7f {
			// A newline or another control character cannot be represented in the
			// profile language, and a path that has one is not a path this
			// applier should be splicing into a profile at all.
			return "", fmt.Errorf("writable root %q contains a control character", root)
		}
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve writable root %q: %w", root, err)
	}
	return resolved, nil
}

// seatbeltQuote renders one path as a Seatbelt string literal. Only the two
// characters the language gives meaning are escaped, so a directory whose name
// contains a quote is a rule rather than an injection.
func seatbeltQuote(path string) string {
	var b strings.Builder
	b.Grow(len(path) + 2)
	b.WriteByte('"')
	for _, r := range path {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}

var (
	seatbeltProbeOnce sync.Once
	seatbeltOK        bool
)

// commandConfinementAvailable answers the launcher-capability question with a
// smoke run rather than a file existence check.
//
// The distinction decides the default of confine_commands, and the launcher
// fails closed, so a false positive would turn confinement on and then refuse
// every command in the session. A binary that is present but cannot apply a
// profile — removed by policy, blocked by the OS, or a future release that drops
// the interface — must therefore answer unavailable.
func commandConfinementAvailable() bool {
	seatbeltProbeOnce.Do(func() {
		binary, err := seatbeltExecPath()
		if err != nil {
			return
		}
		seatbeltOK = probeSeatbelt(binary)
	})
	return seatbeltOK
}

// probeSeatbelt runs one throwaway command under an allow-all profile. It
// answers "can this binary apply a profile" instead of "does this file exist",
// which is the difference between a real capability answer and a false positive
// that would refuse every command in the session.
func probeSeatbelt(binary string) bool {
	return exec.Command(binary, "-p", seatbeltAllowAll, "/usr/bin/true").Run() == nil
}

// seatbeltExecPath resolves the applier. It is cheap and uncached on purpose:
// the launcher calls it in a process of its own for every command, where a smoke
// run would cost more than the command itself.
func seatbeltExecPath() (string, error) {
	binary, err := exec.LookPath(sandboxExecPath)
	if err != nil {
		return "", fmt.Errorf("%s is not available: %w", sandboxExecPath, err)
	}
	return binary, nil
}

// runConfined installs the boundary and replaces this process with the command.
// It only returns when it could not, and then reports the failure itself.
func runConfined(spec launcherSpec) int {
	profile, err := seatbeltProfile(spec.mode, spec.roots)
	if err != nil {
		return launcherFail("cannot apply the file boundary: %v", err)
	}

	binary, err := seatbeltExecPath()
	if err != nil {
		return launcherFail("cannot apply the file boundary: %v", err)
	}

	// The command is resolved here, before the exec, because sandbox-exec
	// reports a missing one as its own status 71 with its own message, which is
	// not this launcher's contract. Resolving it also leaves the argv exactly as
	// the caller wrote it: sandbox-exec execvp's argv[0], so `bash` is found
	// through PATH and the command's own argv[0] stays `bash` — the same thing
	// the Linux launcher produces.
	if _, err := exec.LookPath(spec.argv[0]); err != nil {
		return launcherFail("cannot find %q: %v", spec.argv[0], err)
	}

	argv := make([]string, 0, 3+len(spec.argv))
	argv = append(argv, binary, "-p", profile)
	argv = append(argv, spec.argv...)
	if err := unix.Exec(binary, argv, os.Environ()); err != nil {
		// The exec failed, so the command is not running; saying so is the whole
		// point of the failure status.
		return launcherFail("cannot execute %q: %v", sandboxExecPath, err)
	}
	return 0 // unreachable: Exec replaces the process
}
