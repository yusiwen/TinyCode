package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// prRepo prepares a repository on a feature branch whose upstream exists as a
// remote-tracking ref, with a GitHub origin. Nothing is pushed: the upstream ref
// is created locally, which is all the checks in OpenPullRequest need.
func prRepo(t *testing.T) (repo, branch string) {
	t.Helper()
	repo = t.TempDir()
	for _, cmd := range []string{"git init", "git config user.email t@t", "git config user.name t"} {
		c := exec.Command("sh", "-c", cmd)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", cmd, err, out)
		}
	}
	writeFile(t, filepath.Join(repo, "base.txt"), "base\n")
	gitCommit(t, repo, "init")
	t.Chdir(repo)
	runGit(t, repo, "remote", "add", "origin", "git@github.com:acme/tiny.git")

	branch = "feat/pr"
	runGit(t, repo, "checkout", "-b", branch)
	writeFile(t, filepath.Join(repo, "feat.txt"), "x\n")
	gitCommit(t, repo, "feat: add the file")
	runGit(t, repo, "update-ref", "refs/remotes/origin/"+branch, "HEAD")
	runGit(t, repo, "branch", "--set-upstream-to=origin/"+branch)
	return repo, branch
}

// ghStub puts a fake gh on PATH that records its arguments and prints a URL.
func ghStub(t *testing.T) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args.txt")
	script := filepath.Join(dir, "gh")
	body := "#!/bin/sh\necho \"$@\" > " + argsFile + "\necho https://github.com/acme/tiny/pull/7\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argsFile
}

// TestOpenPullRequestRunsGHWithTheBranchAndBase pins the external call: the
// arguments are exactly what the confirmation dialog described.
func TestOpenPullRequestRunsGHWithTheBranchAndBase(t *testing.T) {
	repo, branch := prRepo(t)
	argsFile := ghStub(t)
	def, err := gitOutput(context.Background(), "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || def == "" {
		t.Fatalf("branch: %v", err)
	}
	wantBase := DefaultBranch(context.Background())
	if wantBase == "" {
		t.Fatal("the default branch was not detected")
	}
	_ = repo

	out, err := OpenPullRequest(context.Background(), PullRequestOptions{Title: "feat: add the file"})
	if err != nil {
		t.Fatalf("OpenPullRequest: %v", err)
	}
	if !strings.Contains(out, "https://github.com/acme/tiny/pull/7") {
		t.Fatalf("output = %q", out)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("gh was not run: %v", err)
	}
	for _, want := range []string{"pr create", "--base " + wantBase, "--head " + branch, "--title feat: add the file"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("gh args are missing %q:\n%s", want, args)
		}
	}
}

// TestOpenPullRequestWithoutGHRunsNothing: no gh, no process, just the URL.
func TestOpenPullRequestWithoutGHRunsNothing(t *testing.T) {
	prRepo(t)
	previous := gitLookPath
	gitLookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	t.Cleanup(func() { gitLookPath = previous })

	out, err := OpenPullRequest(context.Background(), PullRequestOptions{Title: "feat: add the file"})
	if err != nil {
		t.Fatalf("OpenPullRequest: %v", err)
	}
	if !strings.Contains(out, "gh is not installed") {
		t.Fatalf("output = %q", out)
	}
	if !strings.Contains(out, "https://github.com/acme/tiny/compare/") || !strings.Contains(out, "...feat/pr?expand=1") {
		t.Fatalf("the compare URL is missing from %q", out)
	}
}

// TestOpenPullRequestNeedsAPush: a branch the remote does not have produces an
// empty pull request, so it is refused before any external call.
func TestOpenPullRequestNeedsAPush(t *testing.T) {
	prRepo(t)
	// Drop the upstream so the check fires.
	if _, err := gitOutput(context.Background(), "branch", "--unset-upstream"); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenPullRequest(context.Background(), PullRequestOptions{Title: "t"}); err == nil ||
		!strings.Contains(err.Error(), "has no upstream") {
		t.Fatalf("err = %v", err)
	}
}

// TestOpenPullRequestRefusesTheDefaultBranch.
func TestOpenPullRequestRefusesTheDefaultBranch(t *testing.T) {
	repo, _ := prRepo(t)
	def := DefaultBranch(context.Background())
	runGit(t, repo, "checkout", def)
	runGit(t, repo, "update-ref", "refs/remotes/origin/"+def, "HEAD")
	runGit(t, repo, "branch", "--set-upstream-to=origin/"+def)

	if _, err := OpenPullRequest(context.Background(), PullRequestOptions{Title: "t"}); err == nil ||
		!strings.Contains(err.Error(), "refusing to open a pull request from the default branch") {
		t.Fatalf("err = %v", err)
	}
}

// TestOpenPullRequestNeedsATitle.
func TestOpenPullRequestNeedsATitle(t *testing.T) {
	prRepo(t)
	if _, err := OpenPullRequest(context.Background(), PullRequestOptions{}); err == nil ||
		!strings.Contains(err.Error(), "needs a title") {
		t.Fatalf("err = %v", err)
	}
}
