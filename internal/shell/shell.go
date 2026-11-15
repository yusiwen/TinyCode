// Package shell resolves the POSIX shell that runs a command string.
//
// TinyCode runs a command as `bash -c <script>`, or `sh -c` when bash is not on
// PATH, on every platform. Windows is the reason this is a package instead of a
// constant: `cmd.exe` is not a POSIX shell — its quoting, its operators and its
// variable syntax are different — and the command reader (tool/shellread.go), the
// tool description and the models all speak the POSIX one. Spawning `cmd.exe` from
// a tool called `bash` would be a silent lie about what ran.
//
// A Windows host without a POSIX shell therefore gets a refusal that names the
// dependency (Git for Windows ships bash) instead of an ENOENT from inside a tool
// result, which reads like a broken path rather than a missing prerequisite.
package shell

import (
	"context"
	"fmt"
	"os/exec"
)

// candidates are tried in order. bash first because it is what the tool promises
// and what Git for Windows installs; sh is the fallback for minimal Unix systems.
var candidates = []string{"bash", "sh"}

// Resolve returns the argv prefix that runs a script with a POSIX shell:
// {"bash", "-c"} or {"sh", "-c"}. The error says what to install when neither is
// on PATH.
func Resolve() ([]string, error) {
	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil && path != "" {
			return []string{name, "-c"}, nil
		}
	}
	return nil, fmt.Errorf("no POSIX shell on PATH: TinyCode runs commands through `bash -c`, not the platform's command interpreter; install bash (on Windows, Git for Windows ships one)")
}

// Script returns the full argv that runs script.
func Script(script string) ([]string, error) {
	prefix, err := Resolve()
	if err != nil {
		return nil, err
	}
	return append(prefix, script), nil
}

// Command builds an exec.Cmd that runs script in dir (empty means the caller's
// working directory).
func Command(ctx context.Context, script, dir string) (*exec.Cmd, error) {
	prefix, err := Resolve()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, prefix[0], prefix[1], script)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd, nil
}
