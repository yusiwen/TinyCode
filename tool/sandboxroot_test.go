package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// withRunRoot returns a context whose frozen policy names root, which is how a
// delegated task gets its own tree without the process-global configuration
// changing (roadmap C3).
func withRunRoot(root string) context.Context {
	return types.WithSandboxPolicy(context.Background(), types.SandboxPolicy{
		Mode:        types.SandboxWorkspaceWrite,
		ProjectRoot: root,
		Roots:       []string{root},
		Source:      "test",
	})
}

// TestWriteHonoursTheRunRoot: the same tool, two contexts, two trees.
func TestWriteHonoursTheRunRoot(t *testing.T) {
	_, _ = checkpointTestSetup(t)
	other := t.TempDir()

	if _, err := WriteFile().Execute(withRunRoot(other), map[string]any{
		"path": filepath.Join(other, "delegated.txt"), "content": "here\n",
	}); err != nil {
		t.Fatalf("write into the run root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(other, "delegated.txt")); err != nil {
		t.Fatalf("the file did not land in the run root: %v", err)
	}

	// The run root is a boundary too: the configured project is not writable
	// under it. A denial asks for permission when a dialog could be shown, so the
	// test declares itself non-interactive — otherwise it waits for an answer no
	// one can give.
	SetInteractive(false)
	t.Cleanup(func() { SetInteractive(true) })
	// A third directory is outside both the run root and the configured one, so
	// nothing in the process's allow list can explain a pass.
	elsewhere := t.TempDir()
	// A refusal is a successful tool call whose result is the refusal text, not
	// an error: the model reads it, the file does not exist.
	out, err := WriteFile().Execute(withRunRoot(other), map[string]any{
		"path": filepath.Join(elsewhere, "escaped.txt"), "content": "no\n",
	})
	if err != nil {
		t.Fatalf("write returned an error instead of a refusal: %v", err)
	}
	if !strings.Contains(out, "outside the project root") {
		t.Fatalf("result = %q", out)
	}
	if _, statErr := os.Stat(filepath.Join(elsewhere, "escaped.txt")); statErr == nil {
		t.Fatal("the file was written outside the run root")
	}
}

// TestReadHonoursTheRunRoot: the delegated tree is readable, the other tree is
// not, through the same tool and two contexts.
func TestReadHonoursTheRunRoot(t *testing.T) {
	_, _ = checkpointTestSetup(t)
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "mine.txt"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := ReadFile().Execute(withRunRoot(other), map[string]any{"path": filepath.Join(other, "mine.txt")})
	if err != nil || !strings.Contains(out, "mine") {
		t.Fatalf("read = %q, %v", out, err)
	}
}

// TestBashDefaultsToTheRunRoot: a command without a workdir runs in the
// delegated tree.
func TestBashDefaultsToTheRunRoot(t *testing.T) {
	_, _ = checkpointTestSetup(t)
	other := t.TempDir()

	out, err := Bash().Execute(withRunRoot(other), map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	// The fence resolves symlinks (/var → /private/var on macOS), so compare the
	// resolved form.
	want, err := filepath.EvalSymlinks(other)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, want) {
		t.Fatalf("pwd = %q, want %q", out, want)
	}
}

// TestRunRootFallsBackToTheConfiguredRoot: a context without a policy behaves
// exactly as before.
func TestRunRootFallsBackToTheConfiguredRoot(t *testing.T) {
	project, _ := checkpointTestSetup(t)

	if _, err := WriteFile().Execute(context.Background(), map[string]any{
		"path": filepath.Join(project, "plain.txt"), "content": "plain\n",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "plain.txt")); err != nil {
		t.Fatalf("the file did not land in the configured root: %v", err)
	}
}
