package tool

import (
	"reflect"
	"testing"
)

// TestSandboxLauncherInvocation pins the argv contract both sides depend on:
// the launcher command, the mode, one --allow per root, then the command after
// the separator.
func TestSandboxLauncherInvocation(t *testing.T) {
	got := SandboxLauncherInvocation("/bin/tinycode", "workspace-write",
		[]string{"/work", "/tmp"},
		[]string{"bash", "-c", "echo hi"})

	want := []string{
		"/bin/tinycode", SandboxLauncherCommand,
		"--mode", "workspace-write",
		"--allow", "/work",
		"--allow", "/tmp",
		"--", "bash", "-c", "echo hi",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("invocation = %q, want %q", got, want)
	}
}

// TestParseSandboxLauncherArgs covers the strictness the contract needs: a
// malformed invocation must not degrade into running the command with whatever
// boundary happened to parse.
func TestParseSandboxLauncherArgs(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		spec, err := parseSandboxLauncherArgs([]string{
			"--mode", "read-only", "--allow", "/work", "--", "bash", "-c", "true",
		})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if spec.mode != "read-only" {
			t.Errorf("mode = %q, want read-only", spec.mode)
		}
		if !reflect.DeepEqual(spec.roots, []string{"/work"}) {
			t.Errorf("roots = %q, want [/work]", spec.roots)
		}
		if !reflect.DeepEqual(spec.argv, []string{"bash", "-c", "true"}) {
			t.Errorf("argv = %q", spec.argv)
		}
	})

	bad := []struct {
		name string
		args []string
	}{
		{"no separator", []string{"--mode", "read-only", "bash"}},
		{"no command", []string{"--mode", "read-only", "--"}},
		{"unknown option", []string{"--mode", "read-only", "--nope", "--", "bash"}},
		{"positional before separator", []string{"bash", "--mode", "read-only"}},
		{"mode without value", []string{"--mode"}},
		{"allow without value", []string{"--allow"}},
		{"empty allow", []string{"--allow", "", "--", "bash"}},
		{"unsupported mode", []string{"--mode", "yolo", "--", "bash"}},
		{"missing mode", []string{"--", "bash"}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseSandboxLauncherArgs(tc.args); err == nil {
				t.Fatalf("parse(%q) succeeded, want an error", tc.args)
			}
		})
	}
}

// TestRunSandboxLauncherRejectsBadInvocations checks the entry point reports a
// failure status rather than exec'ing something.
func TestRunSandboxLauncherRejectsBadInvocations(t *testing.T) {
	if code := RunSandboxLauncher([]string{"--mode", "nonsense", "--", "true"}); code != SandboxLauncherFailureExit {
		t.Fatalf("RunSandboxLauncher = %d, want %d", code, SandboxLauncherFailureExit)
	}
}

// TestSandboxLauncherFailedNeedsBothSignals pins the classification rule: the
// exit status alone, or the diagnostic text alone, is something an ordinary
// command can produce by accident, so neither may be read as a sandbox report.
func TestSandboxLauncherFailedNeedsBothSignals(t *testing.T) {
	cases := []struct {
		name     string
		exitCode int
		stderr   string
		want     bool
	}{
		{"both", SandboxLauncherFailureExit, SandboxLauncherDiagnosticPrefix + "cannot apply", true},
		{"prefix buried in command output", SandboxLauncherFailureExit, "before\n" + SandboxLauncherDiagnosticPrefix + "x", true},
		{"exit only", SandboxLauncherFailureExit, "command said permission denied", false},
		{"prefix only", 1, SandboxLauncherDiagnosticPrefix + "looks like us", false},
		{"clean run", 0, "", false},
		{"command exit", 130, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SandboxLauncherFailed(tc.exitCode, tc.stderr); got != tc.want {
				t.Fatalf("SandboxLauncherFailed(%d, %q) = %v, want %v",
					tc.exitCode, tc.stderr, got, tc.want)
			}
		})
	}
}
