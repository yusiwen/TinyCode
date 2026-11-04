package verify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// project builds a temp directory with the given files.
func project(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestDiscoverPicksTheMarker: each marker file names its command, and a configured
// command wins over all of them.
func TestDiscoverPicksTheMarker(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"go module", map[string]string{"go.mod": "module m\n"}, "go test ./..."},
		{"npm script", map[string]string{"package.json": `{"scripts":{"test":"jest"}}`}, "npm test --silent"},
		{"make target", map[string]string{"Makefile": "test:\n\tgo test ./...\n"}, "make test"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := project(t, tc.files)
			got, err := Discover(dir, Config{})
			if err != nil || got != tc.want {
				t.Fatalf("Discover = %q, %v; want %q", got, err, tc.want)
			}
		})
	}

	t.Run("config wins", func(t *testing.T) {
		dir := project(t, map[string]string{"go.mod": "module m\n"})
		got, err := Discover(dir, Config{Command: "make check"})
		if err != nil || got != "make check" {
			t.Fatalf("Discover = %q, %v", got, err)
		}
	})

	t.Run("nothing found", func(t *testing.T) {
		dir := project(t, map[string]string{"README": "no tests here\n"})
		_, err := Discover(dir, Config{})
		if err == nil || !strings.Contains(err.Error(), "test.command") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("npm without a test script", func(t *testing.T) {
		dir := project(t, map[string]string{"package.json": `{"scripts":{"build":"tsc"}}`})
		if _, err := Discover(dir, Config{}); err == nil {
			t.Fatal("package.json without a test script was accepted")
		}
	})
}

// TestRunPassAndFail: the report carries the tail a reader needs.
func TestRunPassAndFail(t *testing.T) {
	dir := project(t, nil)

	pass, err := Run(context.Background(), dir, "echo all good", Options{})
	if err != nil || !pass.Passed || pass.ExitCode != 0 {
		t.Fatalf("pass = %+v, %v", pass, err)
	}
	if !strings.Contains(pass.Summary(), "tests passed") || !strings.Contains(pass.Output, "all good") {
		t.Fatalf("pass report = %+v", pass)
	}

	fail, err := Run(context.Background(), dir, "echo '--- FAIL: TestThing'; exit 1", Options{})
	if err != nil || fail.Passed || fail.ExitCode != 1 {
		t.Fatalf("fail = %+v, %v", fail, err)
	}
	if !strings.Contains(fail.Summary(), "tests failed") || !strings.Contains(fail.Output, "TestThing") {
		t.Fatalf("fail report = %+v", fail)
	}
}

// TestRunBoundsTheTail: a failing suite cannot flood the context.
func TestRunBoundsTheTail(t *testing.T) {
	dir := project(t, nil)
	report, err := Run(context.Background(), dir, "for i in $(seq 1 500); do echo line-$i; done; exit 1", Options{MaxOutput: 200})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(report.Output) > 200+len("… (earlier output omitted)\n") {
		t.Fatalf("output is %d bytes", len(report.Output))
	}
	if !strings.Contains(report.Output, "line-500") {
		t.Fatalf("the tail does not hold the end:\n%s", report.Output)
	}
}

// TestRunBoundsTheTime: a hanging command is killed and reported as a timeout, not
// as a test failure. The trailing `true` keeps the shell alive so the child holds
// the output pipe — the case WaitDelay exists for.
func TestRunBoundsTheTime(t *testing.T) {
	dir := project(t, nil)
	start := time.Now()
	report, err := Run(context.Background(), dir, "sleep 5; true", Options{Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.TimedOut || report.Passed {
		t.Fatalf("report = %+v", report)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the bound took %s", elapsed)
	}
	if !strings.Contains(report.Summary(), "timed out") {
		t.Fatalf("summary = %q", report.Summary())
	}
}

// TestRunAGofailingModule is the end-to-end case: discovery plus a real suite.
func TestRunAGofailingModule(t *testing.T) {
	if _, err := os.Stat("/usr/bin/env"); err != nil {
		t.Skip("no shell")
	}
	dir := project(t, map[string]string{
		"go.mod": "module example.test/m\n\ngo 1.27\n",
		"m_test.go": `package m

import "testing"

func TestBroken(t *testing.T) { t.Fatal("boom") }
`,
	})
	command, err := Discover(dir, Config{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	report, err := Run(context.Background(), dir, command, Options{Timeout: 60 * time.Second})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Passed || !strings.Contains(report.Output, "TestBroken") {
		t.Fatalf("report = %+v", report)
	}
}

// TestIsWriteTool: the auto mode's set, with bash deliberately out.
func TestIsWriteTool(t *testing.T) {
	for _, name := range []string{"write_file", "edit", "apply_patch"} {
		if !IsWriteTool(name) {
			t.Errorf("%s is not a write tool", name)
		}
	}
	for _, name := range []string{"bash", "read_file", "git_commit"} {
		if IsWriteTool(name) {
			t.Errorf("%s is a write tool", name)
		}
	}
}
