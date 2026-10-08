package tool

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/yusiwen/tinycode/types"
)

// This file defines the contract between the agent and the confinement
// launcher it re-execs. The launcher is this same binary: applying a kernel
// boundary has to happen after the fork and before the exec of the command,
// which no os/exec call can express, and a re-exec entry point also means the
// exit codes are ours to define.
const (
	// SandboxLauncherCommand is the first argument that turns this binary into
	// the launcher instead of the agent. It is intercepted at the top of main,
	// before any flag parsing, so nothing else can claim it.
	SandboxLauncherCommand = "__sandbox-exec"

	// SandboxLauncherFailureExit is the launcher's own exit status: the
	// boundary could not be applied, so the command did NOT run. It is distinct
	// from any status the command could produce, and consumers must require the
	// exit code together with the diagnostic prefix below — either alone can be
	// produced by an ordinary command by accident.
	SandboxLauncherFailureExit = 125

	// SandboxLauncherDiagnosticPrefix marks a line the launcher itself wrote.
	// The launcher never parses a command's stderr, and a consumer must never
	// treat the command's stderr as a report from the sandbox.
	SandboxLauncherDiagnosticPrefix = "tinycode-sandbox: "
)

// launcherSpec is one parsed launcher invocation.
type launcherSpec struct {
	mode  string
	roots []string
	argv  []string
}

// SandboxLauncherInvocation builds the argv that runs argv under the boundary:
// this binary, the launcher command, the mode, one --allow per writable root,
// then the command after a separator.
//
// self must be the running binary's own path (os.Executable), not a name
// resolved through PATH: the launcher has to be this exact build.
func SandboxLauncherInvocation(self, mode string, roots []string, argv []string) []string {
	args := make([]string, 0, 4+2*len(roots)+len(argv))
	args = append(args, self, SandboxLauncherCommand, "--mode", mode)
	for _, root := range roots {
		args = append(args, "--allow", root)
	}
	args = append(args, "--")
	return append(args, argv...)
}

// parseSandboxLauncherArgs reads the launcher's own arguments. It is strict on
// purpose: a malformed invocation must fail rather than run the command with a
// boundary the caller did not ask for.
func parseSandboxLauncherArgs(args []string) (launcherSpec, error) {
	spec := launcherSpec{}
	seenSeparator := false

	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--":
			spec.argv = append([]string(nil), args[i+1:]...)
			seenSeparator = true
		case args[i] == "--mode":
			if i+1 >= len(args) {
				return launcherSpec{}, fmt.Errorf("--mode needs a value")
			}
			i++
			spec.mode = args[i]
		case args[i] == "--allow":
			if i+1 >= len(args) {
				return launcherSpec{}, fmt.Errorf("--allow needs a path")
			}
			i++
			if args[i] == "" {
				return launcherSpec{}, fmt.Errorf("--allow needs a non-empty path")
			}
			spec.roots = append(spec.roots, args[i])
		case strings.HasPrefix(args[i], "--"):
			return launcherSpec{}, fmt.Errorf("unknown option %q", args[i])
		default:
			return launcherSpec{}, fmt.Errorf("unexpected argument %q before --", args[i])
		}
		if seenSeparator {
			break
		}
	}

	if !seenSeparator {
		return launcherSpec{}, fmt.Errorf("missing -- before the command")
	}
	if len(spec.argv) == 0 {
		return launcherSpec{}, fmt.Errorf("missing command after --")
	}
	switch spec.mode {
	case "read-only", "workspace-write":
	default:
		return launcherSpec{}, fmt.Errorf("unsupported mode %q", spec.mode)
	}
	return spec, nil
}

// launcherFail writes the launcher's own diagnostic and returns the failure
// status. Only this function writes the prefix.
func launcherFail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, SandboxLauncherDiagnosticPrefix+format+"\n", args...)
	return SandboxLauncherFailureExit
}

// RunSandboxLauncher is the launcher entry point. On success it replaces the
// process and never returns; on any failure it reports the reason on its own
// stderr and returns SandboxLauncherFailureExit, so the caller can tell "the
// command did not run" apart from any status the command produces.
func RunSandboxLauncher(args []string) int {
	spec, err := parseSandboxLauncherArgs(args)
	if err != nil {
		return launcherFail("invalid invocation: %v", err)
	}
	return runConfined(spec)
}

// SandboxLauncherFailed reports whether a finished launcher invocation means
// "the boundary was never applied". Both the exit status and the diagnostic
// prefix must match: a command that happens to exit 125, or that prints words
// resembling our diagnostic, must not be mistaken for a launcher failure.
func SandboxLauncherFailed(exitCode int, stderr string) bool {
	return exitCode == SandboxLauncherFailureExit &&
		strings.Contains(stderr, SandboxLauncherDiagnosticPrefix)
}

// SandboxLauncherDiagnostics returns only the lines the launcher itself wrote,
// stripped of the prefix. Everything else in a confined run's stderr belongs to
// the command and must never be presented as a report from the sandbox.
func SandboxLauncherDiagnostics(stderr string) []string {
	var lines []string
	for _, line := range strings.Split(stderr, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), SandboxLauncherDiagnosticPrefix); ok {
			lines = append(lines, rest)
		}
	}
	return lines
}

// sandboxSelfPath locates the binary to re-exec as the launcher. It is a
// variable so a test can stand in a script for the launcher; production uses
// the running executable, because the launcher has to be this exact build.
var sandboxSelfPath = os.Executable

// bashInvocation returns the argv that runs one shell command.
//
// Without command confinement it is bash itself, exactly as before. With it, the
// command goes through the launcher under the mode the run is in: plan mode is
// read-only, build mode is workspace-write over the session's writable roots.
func bashInvocation(ctx context.Context, cmdStr string) ([]string, error) {
	bash := []string{"bash", "-c", cmdStr}
	if !ConfineCommands() {
		return bash, nil
	}

	self, err := sandboxSelfPath()
	if err != nil {
		return nil, fmt.Errorf("locate the confinement launcher: %w", err)
	}

	mode := "workspace-write"
	var roots []string
	if types.PlanWriteRestricted(ctx) {
		mode = "read-only"
	} else {
		roots = DefaultSandbox.WritableRoots()
	}
	return SandboxLauncherInvocation(self, mode, roots, bash), nil
}
