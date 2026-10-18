//go:build darwin

package tool

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Environment of the child half below. The script is a constant rather than an
// environment value: the child is this same test binary, so it already has it.
const (
	seatbeltChildEnv   = "TINYCODE_SEATBELT_CHILD"
	seatbeltModeEnv    = "TINYCODE_SEATBELT_MODE"
	seatbeltRootsEnv   = "TINYCODE_SEATBELT_ROOTS"
	seatbeltInsideEnv  = "TINYCODE_SEATBELT_INSIDE"
	seatbeltOutsideEnv = "TINYCODE_SEATBELT_OUTSIDE"
	seatbeltArgvEnv    = "TINYCODE_SEATBELT_ARGV"
)

// seatbeltWriteScript names the stage in its exit status, which is what makes a
// failure readable from the parent: 12 the inside write was refused, 13 the
// outside write was admitted, 0 exactly the boundary a workspace-write run
// should produce.
const seatbeltWriteScript = `if ! printf inside > "$TINYCODE_SEATBELT_INSIDE"; then exit 12; fi
if printf outside > "$TINYCODE_SEATBELT_OUTSIDE"; then exit 13; fi
exit 0`

// TestSeatbeltProfileDeniesBeforeItGrants pins the shape of the profile. The
// order is the load-bearing part: a Seatbelt profile starts from (allow
// default), so a deny written after the grants would remove them and a profile
// with no deny at all reads as restrictive while denying nothing.
func TestSeatbeltProfileDeniesBeforeItGrants(t *testing.T) {
	root := t.TempDir()

	profile, err := seatbeltProfile("workspace-write", []string{root})
	if err != nil {
		t.Fatalf("seatbeltProfile: %v", err)
	}
	deny := strings.Index(profile, "(deny file-write*)")
	if deny < 0 {
		t.Fatalf("profile does not deny file writes:\n%s", profile)
	}
	grant := strings.Index(profile, "(subpath ")
	if grant < 0 {
		t.Fatalf("profile does not grant the root:\n%s", profile)
	}
	if deny > grant {
		t.Fatalf("the deny comes after the grant, so it would remove it:\n%s", profile)
	}
	if !strings.HasPrefix(profile, "(version 1)") {
		t.Fatalf("profile does not start with its version:\n%s", profile)
	}

	// /dev/null is granted in every mode: a plain `> /dev/null` is refused under
	// (deny file-write*) without it, and that is in every second command.
	if !strings.Contains(profile, "(literal \"/dev/null\")") {
		t.Fatalf("profile does not keep /dev/null writable:\n%s", profile)
	}

	readOnly, err := seatbeltProfile("read-only", []string{root})
	if err != nil {
		t.Fatalf("seatbeltProfile(read-only): %v", err)
	}
	if strings.Contains(readOnly, "(subpath ") {
		t.Fatalf("read-only profile grants a path:\n%s", readOnly)
	}
	if !strings.Contains(readOnly, "(deny file-write*)") {
		t.Fatalf("read-only profile does not deny writes:\n%s", readOnly)
	}
}

// TestSeatbeltProfileResolvesASymlinkedRoot is the control for the resolution:
// Seatbelt matches the path the kernel resolved, so a rule written for the link
// matches nothing. This host's /tmp is exactly that case (a link to
// /private/tmp), and t.TempDir() inherits it through /var.
func TestSeatbeltProfileResolvesASymlinkedRoot(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	resolvedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	resolvedLink, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if resolvedTarget != resolvedLink {
		t.Fatalf("the control is wrong: the link resolves to %q, the target to %q", resolvedLink, resolvedTarget)
	}

	profile, err := seatbeltProfile("workspace-write", []string{link})
	if err != nil {
		t.Fatalf("seatbeltProfile: %v", err)
	}
	if !strings.Contains(profile, seatbeltQuote(resolvedLink)) {
		t.Fatalf("profile does not carry the resolved root %q:\n%s", resolvedLink, profile)
	}
	// The link itself must not appear as a rule: it would match no path, so a
	// profile that carried both forms would hide the bug behind the correct one.
	if strings.Contains(profile, seatbeltQuote(link)) {
		t.Fatalf("profile carries the unresolved root %q:\n%s", link, profile)
	}
}

// TestSeatbeltQuoteCannotEscapeItsRule pins that a directory whose name contains
// a quote produces a rule rather than a dangling profile.
func TestSeatbeltQuoteCannotEscapeItsRule(t *testing.T) {
	odd := filepath.Join(t.TempDir(), `we"ird \ dir`)
	if err := os.Mkdir(odd, 0o755); err != nil {
		t.Fatalf("cannot create the odd directory: %v", err)
	}

	profile, err := seatbeltProfile("workspace-write", []string{odd})
	if err != nil {
		t.Fatalf("seatbeltProfile: %v", err)
	}
	line := ""
	for _, candidate := range strings.Split(profile, "\n") {
		if strings.Contains(candidate, "we") {
			line = candidate
		}
	}
	if line == "" {
		t.Fatalf("profile has no rule for the odd root:\n%s", profile)
	}
	if !strings.Contains(line, `\"`) || !strings.Contains(line, `\\`) {
		t.Fatalf("rule is not escaped: %s", line)
	}
	// The rule must still be one balanced form: one opening, one closing quote
	// after the escaped pair is accounted for.
	if strings.Count(strings.ReplaceAll(line, `\"`, ""), `"`) != 2 {
		t.Fatalf("rule is not a single quoted literal: %s", line)
	}
}

// TestSeatbeltProfileRefusesARootItCannotUse pins fail-closed: a root that
// cannot be represented or resolved stops the launcher rather than narrowing the
// boundary silently.
func TestSeatbeltProfileRefusesARootItCannotUse(t *testing.T) {
	t.Run("control character", func(t *testing.T) {
		if _, err := seatbeltProfile("workspace-write", []string{"/tmp/with\nnewline"}); err == nil {
			t.Fatal("a root containing a newline was accepted")
		}
	})
	t.Run("missing root", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "not-there")
		if _, err := seatbeltProfile("workspace-write", []string{missing}); err == nil {
			t.Fatal("a root that does not exist was accepted")
		}
	})
	t.Run("empty root", func(t *testing.T) {
		if _, err := seatbeltProfile("workspace-write", []string{""}); err == nil {
			t.Fatal("an empty root was accepted")
		}
	})
	t.Run("control: a real root is accepted", func(t *testing.T) {
		if _, err := seatbeltProfile("workspace-write", []string{t.TempDir()}); err != nil {
			t.Fatalf("a real root was refused: %v", err)
		}
	})
}

// TestProbeSeatbeltIsASmokeRun pins that the capability answer is a run and not
// a file check: /bin/false exists and can be executed, and cannot apply a
// profile.
func TestProbeSeatbeltIsASmokeRun(t *testing.T) {
	if probeSeatbelt("/bin/false") {
		t.Fatal("a binary that cannot apply a profile was reported as usable")
	}
	if probeSeatbelt(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Fatal("a missing binary was reported as usable")
	}
	binary, err := seatbeltExecPath()
	if err != nil {
		t.Skipf("no %s on this host: %v", sandboxExecPath, err)
	}
	if !probeSeatbelt(binary) {
		t.Fatalf("%s is present but the probe says it cannot apply a profile", binary)
	}
}

// TestSeatbeltChildHelper is not a test: it is the child half of
// TestSeatbeltBoundaryIsEnforced. On macOS the boundary cannot be applied to the
// running image at all — sandbox-exec is a separate program — so the child does
// what the launcher does and replaces itself with the confined command.
func TestSeatbeltChildHelper(t *testing.T) {
	if os.Getenv(seatbeltChildEnv) != "1" {
		t.Skip("child half of TestSeatbeltBoundaryIsEnforced")
	}
	roots := []string{}
	if raw := os.Getenv(seatbeltRootsEnv); raw != "" {
		roots = strings.Split(raw, "\n")
	}
	argv := []string{"/bin/sh", "-c", seatbeltWriteScript}
	if raw := os.Getenv(seatbeltArgvEnv); raw != "" {
		argv = strings.Split(raw, "\n")
	}
	code := runConfined(launcherSpec{
		mode:  os.Getenv(seatbeltModeEnv),
		roots: roots,
		argv:  argv,
	})
	// Reaching this line means the exec did not happen; runConfined has already
	// written its own diagnostic and its status is the readable part.
	os.Exit(code)
}

// TestSeatbeltBoundaryIsEnforced is the acceptance test for the mechanism: a
// command confined to one root writes inside it and cannot write outside it.
//
// The control run is the part that makes it evidence: the same script, unconfined,
// writes both files. Without it a failure caused by the environment (a temporary
// directory that is not writable, a path typo) would read as a working boundary
// for the outside half.
func TestSeatbeltBoundaryIsEnforced(t *testing.T) {
	if !commandConfinementAvailable() {
		t.Skipf("no usable %s on this host", sandboxExecPath)
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

	child := func(mode string, roots []string) (int, string) {
		cmd := exec.Command(os.Args[0], "-test.run=TestSeatbeltChildHelper")
		cmd.Env = append(os.Environ(),
			seatbeltChildEnv+"=1",
			seatbeltModeEnv+"="+mode,
			seatbeltRootsEnv+"="+strings.Join(roots, "\n"),
			seatbeltInsideEnv+"="+inside,
			seatbeltOutsideEnv+"="+outside,
		)
		out, err := cmd.CombinedOutput()
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("cannot run the child: %v", err)
		}
		return code, string(out)
	}
	clear := func() {
		for _, path := range []string{inside, outside} {
			os.Remove(path)
		}
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}

	t.Run("control writes both files unconfined", func(t *testing.T) {
		clear()
		cmd := exec.Command("/bin/sh", "-c", seatbeltWriteScript)
		cmd.Env = append(os.Environ(),
			seatbeltInsideEnv+"="+inside,
			seatbeltOutsideEnv+"="+outside,
		)
		out, err := cmd.CombinedOutput()
		if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 13 {
			t.Fatalf("the control did not admit both writes (%v):\n%s", err, out)
		}
		if !exists(inside) || !exists(outside) {
			t.Fatalf("the control wrote neither or only one file (inside=%v outside=%v)", exists(inside), exists(outside))
		}
	})

	t.Run("workspace-write admits inside and refuses outside", func(t *testing.T) {
		clear()
		code, out := child("workspace-write", []string{root})
		if code != 0 {
			t.Fatalf("child exit %d (12 = inside refused, 13 = outside admitted):\n%s", code, out)
		}
		if !exists(inside) {
			t.Fatal("the inside file was not written: the boundary narrowed below what was granted")
		}
		if exists(outside) {
			t.Fatal("the outside file was written: the boundary did not hold")
		}
	})

	t.Run("read-only refuses inside as well", func(t *testing.T) {
		clear()
		code, out := child("read-only", nil)
		if code != 12 {
			t.Fatalf("child exit %d, want 12 (the inside write refused):\n%s", code, out)
		}
		if exists(inside) || exists(outside) {
			t.Fatalf("read-only wrote a file (inside=%v outside=%v)", exists(inside), exists(outside))
		}
	})
}

// TestSeatbeltAcceptsAnOddRootAtTheKernel is the mutation guard for the profile
// renderer's escaping: a root whose name contains a quote has to be *usable*, not
// merely representable. The rendered profile is handed to the real applier, so a
// rule that spliced the path unquoted shows up as a failed exec rather than as a
// substring a unit test happens to like.
func TestSeatbeltAcceptsAnOddRootAtTheKernel(t *testing.T) {
	if !commandConfinementAvailable() {
		t.Skipf("no usable %s on this host", sandboxExecPath)
	}
	odd := filepath.Join(t.TempDir(), `we"ird`)
	if err := os.Mkdir(odd, 0o755); err != nil {
		t.Fatalf("cannot create the odd directory: %v", err)
	}
	profile, err := seatbeltProfile("workspace-write", []string{odd})
	if err != nil {
		t.Fatalf("seatbeltProfile: %v", err)
	}
	target := filepath.Join(odd, "inside.txt")
	out, err := exec.Command(sandboxExecPath, "-p", profile, "/bin/sh", "-c",
		fmt.Sprintf("printf ok > %s", shellQuote(target))).CombinedOutput()
	if err != nil {
		t.Fatalf("a confined write into a root with a quote in its name failed: %v\n%s\nprofile:\n%s", err, out, profile)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("the write reported success but the file is missing: %v", err)
	}
}

// TestCommandConfinementProbeFailsClosedOnAnUnusableApplier is the safety
// property, not the happy path: this answer decides the default of
// confine_commands, and the launcher refuses every command when the boundary
// cannot be applied. An applier that exists but cannot apply a profile must
// therefore read as unavailable rather than turning confinement on.
func TestCommandConfinementProbeFailsClosedOnAnUnusableApplier(t *testing.T) {
	previous := seatbeltResolver
	seatbeltResolver = func() (string, error) { return "/bin/false", nil }
	t.Cleanup(func() {
		seatbeltResolver = previous
		resetSeatbeltProbeForTest()
	})
	resetSeatbeltProbeForTest()

	if commandConfinementAvailable() {
		t.Fatal("/bin/false was reported as a usable applier")
	}
}

// TestSeatbeltLauncherRefusesAMissingCommand pins the contract against
// sandbox-exec's own behaviour: it reports a missing command as exit 71 with its
// own message, which a caller could mistake for the command's status. The
// launcher resolves the command first so the refusal is its own 125 plus the
// diagnostic prefix — the pair that means "the command did not run".
func TestSeatbeltLauncherRefusesAMissingCommand(t *testing.T) {
	if !commandConfinementAvailable() {
		t.Skipf("no usable %s on this host", sandboxExecPath)
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestSeatbeltChildHelper")
	cmd.Env = append(os.Environ(),
		seatbeltChildEnv+"=1",
		seatbeltModeEnv+"=workspace-write",
		seatbeltArgvEnv+"=definitely-not-a-command-xyz",
	)
	out, err := cmd.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("the launcher did not fail (%v):\n%s", err, out)
	}
	if got := exitErr.ExitCode(); got != SandboxLauncherFailureExit {
		t.Fatalf("exit = %d, want %d (%s):\n%s", got, SandboxLauncherFailureExit, SandboxLauncherDiagnosticPrefix, out)
	}
	if !strings.Contains(string(out), SandboxLauncherDiagnosticPrefix) {
		t.Fatalf("the refusal carries no %q diagnostic:\n%s", SandboxLauncherDiagnosticPrefix, out)
	}
	if !SandboxLauncherFailed(exitErr.ExitCode(), string(out)) {
		t.Fatalf("the contract does not classify its own refusal as a launcher failure:\n%s", out)
	}
}
