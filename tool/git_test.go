package tool

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, cmd := range []string{"git init", "git config user.email test@test", "git config user.name test"} {
		c := exec.Command("sh", "-c", cmd)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", cmd, err, out)
		}
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func gitCommit(t *testing.T, dir, msg string) {
	t.Helper()
	c := exec.Command("sh", "-c", "git add -A && git commit -m '"+msg+"'")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
}

func TestGitStatus(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)

	tool := GitStatus()
	// No changes
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("GitStatus: %v", err)
	}
	if !strings.Contains(result, "nothing to commit") && !strings.Contains(result, "initial commit") {
		t.Logf("got: %s", result)
	}
}

func TestGitBranch(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)

	// Need at least one commit for branch list
	writeFile(t, dir+"/.gitkeep", "")
	gitCommit(t, dir, "init")

	tool := GitBranch()
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("GitBranch: %v", err)
	}
	if !strings.Contains(result, "master") && !strings.Contains(result, "main") {
		t.Errorf("expected master/main branch, got: %s", result)
	}
}

func TestGitCommit(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)

	writeFile(t, dir+"/test.txt", "hello")
	tool := GitCommit()
	result, err := tool.Execute(context.Background(), map[string]any{"message": "initial"})
	if err != nil {
		t.Fatalf("GitCommit: %v", err)
	}
	if !strings.Contains(result, "Committed") && !strings.Contains(result, "initial") {
		t.Errorf("expected commit succeeded, got: %s", result)
	}
}

func TestGitLog(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)

	writeFile(t, dir+"/a.txt", "a")
	gitCommit(t, dir, "first")
	writeFile(t, dir+"/b.txt", "b")
	gitCommit(t, dir, "second")

	tool := GitLog()
	result, err := tool.Execute(context.Background(), map[string]any{"limit": float64(2)})
	if err != nil {
		t.Fatalf("GitLog: %v", err)
	}
	if !strings.Contains(result, "second") {
		t.Errorf("expected 'second' in log, got: %s", result)
	}
}

func TestGitDiff(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	wd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(wd)

	writeFile(t, dir+"/test.go", "package main\nfunc main() {}")
	gitCommit(t, dir, "init")

	// Now modify
	writeFile(t, dir+"/test.go", "package main\nfunc main() {\n\tprintln(\"hello\")\n}")
	tool := GitDiff()
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("GitDiff: %v", err)
	}
	if !strings.Contains(result, "println") {
		t.Errorf("expected diff with println, got:\n%s", result)
	}
}
