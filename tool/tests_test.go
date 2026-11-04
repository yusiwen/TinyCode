package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/verify"
)

// testsProject makes a temp project with one file and returns its path.
func testsProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestRunTestsToolReportsAPass: the tool's result is the report, so the model sees
// the command and the outcome.
func TestRunTestsToolReportsAPass(t *testing.T) {
	dir := testsProject(t, map[string]string{"go.mod": "module m\n"})
	tool := RunTests(verify.Settings{Dir: dir, Config: verify.Config{Command: "echo suite-ok"}})

	out, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("run_tests: %v", err)
	}
	if !strings.Contains(out, "tests passed") || !strings.Contains(out, "suite-ok") {
		t.Fatalf("output = %q", out)
	}
}

// TestRunTestsToolKeepsTheFailureTail: a failing suite names its failure and stays
// within the cap.
func TestRunTestsToolKeepsTheFailureTail(t *testing.T) {
	dir := testsProject(t, nil)
	tool := RunTests(verify.Settings{Dir: dir, Config: verify.Config{
		Command:        "echo '--- FAIL: TestBroken'; exit 1",
		MaxOutputBytes: 80,
	}})

	out, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("run_tests: %v", err)
	}
	if !strings.Contains(out, "tests failed") || !strings.Contains(out, "TestBroken") {
		t.Fatalf("output = %q", out)
	}
}

// TestRunTestsToolWithoutACommand: the model is told how to fix it.
func TestRunTestsToolWithoutACommand(t *testing.T) {
	dir := testsProject(t, map[string]string{"README": "nothing\n"})
	tool := RunTests(verify.Settings{Dir: dir})

	if _, err := tool.Execute(context.Background(), map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "test.command") {
		t.Fatalf("err = %v", err)
	}
}
