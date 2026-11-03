package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// remoteRepo prepares a repository with a commit on a feature branch and a bare
// remote named origin. It returns the repository and the bare remote.
func remoteRepo(t *testing.T) (repo, bare string) {
	t.Helper()
	repo = t.TempDir()
	bare = t.TempDir()
	for _, cmd := range []string{"git init", "git config user.email t@t", "git config user.name t"} {
		c := exec.Command("sh", "-c", cmd)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", cmd, err, out)
		}
	}
	c := exec.Command("git", "init", "--bare", bare)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("bare init: %v\n%s", err, out)
	}
	writeFile(t, filepath.Join(repo, "base.txt"), "base\n")
	gitCommit(t, repo, "init")
	t.Chdir(repo)
	runGit(t, repo, "remote", "add", "origin", bare)
	return repo, bare
}

// runGit runs one git command in dir.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestPushBranchPushesAFeatureBranch: the commit lands in the bare remote and the
// upstream is set, which is what /push needs before a pull request.
func TestPushBranchPushesAFeatureBranch(t *testing.T) {
	repo, bare := remoteRepo(t)
	runGit(t, repo, "checkout", "-b", "feat/push")
	writeFile(t, filepath.Join(repo, "feat.txt"), "x\n")
	gitCommit(t, repo, "feat")

	out, err := PushBranch(context.Background(), false)
	if err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if !strings.Contains(out, "Pushed feat/push") {
		t.Fatalf("output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(bare, "refs", "heads", "feat", "push")); err != nil {
		t.Fatalf("the branch did not land in the bare remote: %v", err)
	}
	upstream, err := gitOutput(context.Background(), "rev-parse", "--abbrev-ref", "@{u}")
	if err != nil {
		t.Fatalf("upstream: %v", err)
	}
	if upstream != "origin/feat/push" {
		t.Fatalf("upstream = %q", upstream)
	}
}

// TestPushBranchRefusesTheDefaultBranch: the repository's rule is branch first.
func TestPushBranchRefusesTheDefaultBranch(t *testing.T) {
	_, _ = remoteRepo(t)
	def, err := CurrentBranch(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := PushBranch(context.Background(), false); err == nil ||
		!strings.Contains(err.Error(), "refusing to push the default branch") {
		t.Fatalf("err = %v", err)
	}
	// A force does not open a hole: the default branch is refused either way.
	if _, err := PushBranch(context.Background(), true); err == nil ||
		!strings.Contains(err.Error(), "refusing to push the default branch") {
		t.Fatalf("force err = %v", err)
	}
	if def == "" {
		t.Fatal("the default branch was not detected")
	}
}

// TestPushBranchWithoutARemote names the situation instead of returning git's
// exit code.
func TestPushBranchWithoutARemote(t *testing.T) {
	repo, _ := remoteRepo(t)
	runGit(t, repo, "checkout", "-b", "feat/no-remote")
	runGit(t, repo, "remote", "remove", "origin")

	if _, err := PushBranch(context.Background(), false); err == nil ||
		!strings.Contains(err.Error(), "no origin remote") {
		t.Fatalf("err = %v", err)
	}
}

// TestPushBranchOnDetachedHead: there is no branch to push.
func TestPushBranchOnDetachedHead(t *testing.T) {
	repo, _ := remoteRepo(t)
	runGit(t, repo, "checkout", "--detach", "HEAD")

	if _, err := PushBranch(context.Background(), false); err == nil ||
		!strings.Contains(err.Error(), "detached") {
		t.Fatalf("err = %v", err)
	}
}

// TestPushBranchForceUsesForceWithLease: a force is bounded by the remote's
// state, and the argument is visible in the error text when it is refused.
func TestPushBranchForceUsesForceWithLease(t *testing.T) {
	repo, bare := remoteRepo(t)
	runGit(t, repo, "checkout", "-b", "feat/force")
	writeFile(t, filepath.Join(repo, "a.txt"), "a\n")
	gitCommit(t, repo, "a")
	if _, err := PushBranch(context.Background(), false); err != nil {
		t.Fatalf("first push: %v", err)
	}

	// Move the remote's branch out from under the local one.
	other := t.TempDir()
	runGit(t, other, "clone", bare, "clone")
	clone := filepath.Join(other, "clone")
	// A clone carries no identity of its own; the suite's HOME has no global one.
	runGit(t, clone, "config", "user.email", "t@t")
	runGit(t, clone, "config", "user.name", "t")
	runGit(t, clone, "checkout", "feat/force")
	writeFile(t, filepath.Join(clone, "b.txt"), "b\n")
	gitCommit(t, clone, "b")
	runGit(t, clone, "push", "origin", "feat/force")

	// A plain force would overwrite it; --force-with-lease refuses because the
	// local ref is stale.
	_, err := PushBranch(context.Background(), true)
	if err == nil {
		t.Fatal("--force-with-lease overwrote a remote that had moved")
	}
	if !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err = %v", err)
	}
}
