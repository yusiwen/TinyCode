package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// withCommandConfinement turns the policy on for one test and restores it.
func withCommandConfinement(t *testing.T, confined bool) {
	t.Helper()
	previous := ConfineCommands()
	SetConfineCommands(confined)
	t.Cleanup(func() { SetConfineCommands(previous) })
}

// withLauncher stands in a script for the confinement launcher and restores the
// real lookup afterwards.
func withLauncher(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "launcher")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := sandboxSelfPath
	sandboxSelfPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { sandboxSelfPath = previous })
}

func TestBashInvocationWithoutConfinement(t *testing.T) {
	withCommandConfinement(t, false)

	got, err := bashInvocation(context.Background(), "echo hi")
	if err != nil {
		t.Fatalf("bashInvocation: %v", err)
	}
	want := []string{"bash", "-c", "echo hi"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// TestBashInvocationConfinedMapsModeAndRoots pins what the boundary is given:
// plan mode is read-only and grants no path, build mode is workspace-write over
// exactly the session's writable roots.
func TestBashInvocationConfinedMapsModeAndRoots(t *testing.T) {
	withCommandConfinement(t, true)

	root := t.TempDir()
	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{
		ProjectRoot:    root,
		AutoAllowPaths: []string{root},
		allowedPaths:   map[string]bool{},
	}
	defer func() { DefaultSandbox = saved }()

	t.Run("plan mode", func(t *testing.T) {
		ctx := types.WithPlanWriteRestriction(context.Background(), true)
		argv, err := bashInvocation(ctx, "echo hi")
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(argv, " ")
		if !strings.Contains(joined, "--mode read-only") {
			t.Fatalf("argv = %q, want the read-only mode", joined)
		}
		if strings.Contains(joined, "--allow") {
			t.Fatalf("argv = %q, want no granted path under read-only", joined)
		}
		if !strings.HasSuffix(joined, "-- bash -c echo hi") {
			t.Fatalf("argv = %q, want the original command after the separator", joined)
		}
	})

	t.Run("build mode", func(t *testing.T) {
		argv, err := bashInvocation(context.Background(), "echo hi")
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(argv, " ")
		if !strings.Contains(joined, "--mode workspace-write") {
			t.Fatalf("argv = %q, want the workspace-write mode", joined)
		}
		for _, r := range DefaultSandbox.WritableRoots() {
			if !strings.Contains(joined, "--allow "+r) {
				t.Fatalf("argv = %q, want --allow %s", joined, r)
			}
		}
	})
}

// TestBashInvocationSurfacesLauncherLookupFailure covers the one failure that
// happens before anything runs: without a launcher path there is no boundary,
// so the command must not be run at all.
func TestBashInvocationSurfacesLauncherLookupFailure(t *testing.T) {
	withCommandConfinement(t, true)

	previous := sandboxSelfPath
	sandboxSelfPath = func() (string, error) { return "", os.ErrNotExist }
	defer func() { sandboxSelfPath = previous }()

	if _, err := bashInvocation(context.Background(), "echo hi"); err == nil {
		t.Fatal("bashInvocation succeeded without a launcher, want an error")
	}
}

// TestBashReportsLauncherFailureAsNotRun is the classification test: when the
// boundary could not be applied, the result says the command did not run, and
// it reads the launcher's own diagnostic rather than whatever the command
// printed.
func TestBashReportsLauncherFailureAsNotRun(t *testing.T) {
	withCommandConfinement(t, true)
	withLauncher(t, "#!/bin/sh\necho 'tinycode-sandbox: stub could not confine' >&2\necho 'command output that never happened'\nexit 125\n")

	result, err := Bash().Execute(context.Background(), map[string]any{"command": "echo hi"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "[SANDBOX]") || !strings.Contains(result, "did not run") {
		t.Fatalf("result = %q, want it to say the command did not run", result)
	}
	if !strings.Contains(result, "stub could not confine") {
		t.Fatalf("result = %q, want the launcher's own diagnostic", result)
	}
	// The command never ran, so its output must not be presented as if it had.
	if strings.Contains(result, "command output that never happened") {
		t.Fatalf("result = %q, want no command output attributed to a command that did not run", result)
	}
}

// TestBashConfinedCommandBehavesNormally is the other half: with the boundary
// applied successfully, the command runs and its output arrives unchanged.
func TestBashConfinedCommandBehavesNormally(t *testing.T) {
	withCommandConfinement(t, true)
	// A stand-in that applies nothing but does what the real launcher does with
	// the argv: skip its own options, then exec the command.
	withLauncher(t, "#!/bin/sh\nwhile [ $# -gt 0 ] && [ \"$1\" != \"--\" ]; do shift; done\nshift\nexec \"$@\"\n")

	result, err := Bash().Execute(context.Background(), map[string]any{"command": "echo confined-hello"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(result, "confined-hello") {
		t.Fatalf("result = %q, want the command's output", result)
	}
}

// TestSandboxLauncherDiagnosticsFiltersCommandOutput pins that the sandbox's
// report is only what the launcher wrote: a command that prints words
// resembling our diagnostic contributes nothing to it.
func TestSandboxLauncherDiagnosticsFiltersCommandOutput(t *testing.T) {
	stderr := "ordinary command noise\n" +
		SandboxLauncherDiagnosticPrefix + "cannot apply the file boundary: invalid argument\n" +
		"the command printed this\n"

	got := SandboxLauncherDiagnostics(stderr)
	if len(got) != 1 {
		t.Fatalf("diagnostics = %q, want exactly the launcher's line", got)
	}
	if got[0] != "cannot apply the file boundary: invalid argument" {
		t.Fatalf("diagnostic = %q", got[0])
	}
}
