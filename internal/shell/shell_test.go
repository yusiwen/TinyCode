package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestResolvePrefersBash pins the order: the tool promises `bash -c`, and sh is
// the fallback for a Unix system that does not carry bash.
//
// Windows is skipped because a fake shell there needs a real .exe: the fake in
// this test is a shell script, which is exactly what the resolver should find on
// the platforms whose shell is a script.
func TestResolvePrefersBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a fake bash on Windows would have to be a PE binary; the resolver is exercised by the Windows CI job against the real Git Bash")
	}
	dir := t.TempDir()
	for _, name := range []string{"bash", "sh"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)

	argv, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(argv) != 2 || argv[0] != "bash" || argv[1] != "-c" {
		t.Errorf("Resolve = %v, want [bash -c]", argv)
	}

	if _, err := os.Stat(filepath.Join(dir, "bash")); err != nil {
		t.Fatal(err)
	}
	// Without bash, sh is the fallback.
	if err := os.Remove(filepath.Join(dir, "bash")); err != nil {
		t.Fatal(err)
	}
	argv, err = Resolve()
	if err != nil {
		t.Fatalf("Resolve without bash: %v", err)
	}
	if argv[0] != "sh" {
		t.Errorf("fallback = %v, want sh", argv)
	}
}

// TestResolveNamesTheDependencyWhenThereIsNoShell is the refusal a Windows host
// without Git for Windows gets: it has to say what to install, not hand back an
// ENOENT that reads like a broken path.
func TestResolveNamesTheDependencyWhenThereIsNoShell(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := Resolve()
	if err == nil {
		t.Fatal("Resolve found a shell on an empty PATH")
	}
	for _, want := range []string{"POSIX shell", "bash", "Git for Windows"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	if _, err := Command(context.Background(), "echo hi", ""); err == nil {
		t.Error("Command built a command without a shell")
	}
}

// TestScriptRunsThroughTheResolvedShell is the whole point of the package: the
// argv a caller spawns is the shell plus the script, in that order.
func TestScriptRunsThroughTheResolvedShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake shell is a script")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "bash")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s' \"$2\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	argv, err := Script("echo hi")
	if err != nil {
		t.Fatalf("Script: %v", err)
	}
	if len(argv) != 3 || argv[2] != "echo hi" {
		t.Fatalf("Script = %v", argv)
	}
	out, err := exec.Command(argv[0], argv[1], argv[2]).Output()
	if err != nil {
		t.Fatalf("run the fake shell: %v", err)
	}
	if string(out) != "echo hi" {
		t.Errorf("the script did not reach the shell: %q", out)
	}
}
