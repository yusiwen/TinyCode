package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/types"
)

// changesRepo prepares a git repository, points the checkpoint store at a
// throwaway directory and makes it the working directory, because the git tools
// run in the process directory.
func changesRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, cmd := range []string{"git init", "git config user.email t@t", "git config user.name t"} {
		c := exec.Command("sh", "-c", cmd)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", cmd, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", "-c", "git add -A && git commit -m init")
	c.Dir = repo
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}

	previousRoot := tool.CheckpointRoot()
	tool.SetCheckpointRoot(filepath.Join(t.TempDir(), "checkpoints"))
	previousProject := tool.DefaultSandbox.ProjectRoot
	tool.DefaultSandbox.ProjectRoot = repo
	t.Cleanup(func() {
		tool.SetCheckpointRoot(previousRoot)
		tool.DefaultSandbox.ProjectRoot = previousProject
	})
	t.Chdir(repo)
	return repo
}

// toolWrote writes through the write tool with a call scope, which is what makes
// the change attributable to the run.
func toolWrote(t *testing.T, repo, name string) {
	t.Helper()
	ctx := types.WithToolCallScope(context.Background(), types.ToolCallScope{
		Tool: "write_file", Run: 1, Step: 1, Call: 1,
	})
	if _, err := tool.WriteFile().Execute(ctx, map[string]any{
		"path": filepath.Join(repo, name), "content": "one\ntwo\n",
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestChangesCommandPrintsTheWorkingTree: /changes shows the tree and writes
// nothing.
func TestChangesCommandPrintsTheWorkingTree(t *testing.T) {
	repo := changesRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "hand.txt"), []byte("hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestTUI()
	m.runStart = time.Now().Add(-time.Minute)

	m.handleCommand("/changes")

	reply := lastMessage(m)
	for _, want := range []string{"1 file(s) changed", "hand.txt", "other"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply is missing %q:\n%s", want, reply)
		}
	}
}

// TestChangesCommandOutsideARepository reports the situation instead of failing
// silently.
func TestChangesCommandOutsideARepository(t *testing.T) {
	t.Chdir(t.TempDir())
	m := newTestTUI()

	m.handleCommand("/changes")

	if !strings.Contains(lastMessage(m), "not inside a git repository") {
		t.Fatalf("reply = %q", lastMessage(m))
	}
}

// TestNoteRunChangesIsSilentWhenNothingWasWritten pins the "only when there is
// something to say" rule: a run that changed nothing adds no line.
func TestNoteRunChangesIsSilentWhenNothingWasWritten(t *testing.T) {
	changesRepo(t)
	m := newTestTUI()
	before := len(m.messages)

	m.noteRunChanges(nil)

	if len(m.messages) != before {
		t.Fatalf("a silent run appended %d message(s)", len(m.messages)-before)
	}
}

// TestNoteRunChangesAnnouncesThisRunsWrites: the line names the run's own files
// and points at both next commands.
func TestNoteRunChangesAnnouncesThisRunsWrites(t *testing.T) {
	repo := changesRepo(t)
	toolWrote(t, repo, "written.go")
	m := newTestTUI()
	m.runStart = time.Now().Add(-time.Minute)

	m.noteRunChanges(nil)

	reply := lastMessage(m)
	for _, want := range []string{"changed 1 file(s), +2/-0", "/changes", "/commit"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply is missing %q:\n%s", want, reply)
		}
	}
}

// TestNoteRunChangesSkipsAFailedRun: a failed run's writes are visible through
// /changes, but the run-end line is for runs that finished.
func TestNoteRunChangesSkipsAFailedRun(t *testing.T) {
	repo := changesRepo(t)
	toolWrote(t, repo, "written.go")
	m := newTestTUI()
	m.runStart = time.Now().Add(-time.Minute)
	before := len(m.messages)

	m.noteRunChanges(fmt.Errorf("provider failed"))

	if len(m.messages) != before {
		t.Fatalf("a failed run appended a change line")
	}
}

// TestCommitCommandStagesOnlyTheRunsFiles drives the loop end to end: the paths
// shown are the run's, Cancel changes nothing, Commit lands exactly those files.
func TestCommitCommandStagesOnlyTheRunsFiles(t *testing.T) {
	repo := changesRepo(t)
	toolWrote(t, repo, "written.go")
	if err := os.WriteFile(filepath.Join(repo, "hand.txt"), []byte("hand\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestTUI()
	m.runStart = time.Now().Add(-time.Minute)

	m.handleCommand("/commit feat: write the file")

	if !strings.Contains(lastMessage(m), "Staging 1 file(s)") {
		t.Fatalf("staging list = %q", lastMessage(m))
	}
	if m.dialogOnDone == nil || !strings.Contains(m.dialogMsg, "Commit 1 file(s) this run wrote?") {
		t.Fatalf("dialog = %q", m.dialogMsg)
	}

	// Cancel leaves the tree as it was.
	m.dialogOnDone("Cancel")
	if status := gitLines(t, repo, "status", "--porcelain"); !strings.Contains(status, "written.go") {
		t.Fatalf("Cancel committed something:\n%s", status)
	}

	// Commit lands the run's file and nothing else.
	m.handleCommand("/commit feat: write the file")
	m.dialogOnDone("Commit")
	if !strings.Contains(lastMessage(m), "Committed") {
		t.Fatalf("commit reply = %q", lastMessage(m))
	}
	show := gitLines(t, repo, "show", "--name-only", "--format=", "HEAD")
	if strings.TrimSpace(show) != "written.go" {
		t.Fatalf("commit contains %q, want only written.go", show)
	}
	if status := gitLines(t, repo, "status", "--porcelain"); !strings.Contains(status, "hand.txt") {
		t.Fatalf("the unrelated change was swept in:\n%s", status)
	}
}

// TestCommitCommandWithoutAMessageRefuses: no message, no staging.
func TestCommitCommandWithoutAMessageRefuses(t *testing.T) {
	repo := changesRepo(t)
	toolWrote(t, repo, "written.go")
	m := newTestTUI()
	m.runStart = time.Now().Add(-time.Minute)

	m.handleCommand("/commit")

	if !strings.Contains(lastMessage(m), "usage: /commit") {
		t.Fatalf("reply = %q", lastMessage(m))
	}
	if m.dialogOnDone != nil {
		t.Fatal("a dialog opened without a message")
	}
	if status := gitLines(t, repo, "status", "--porcelain"); !strings.Contains(status, "written.go") {
		t.Fatalf("something was staged:\n%s", status)
	}
}

// TestCommitCommandWithNothingThisRunWrote: --all is offered, and nothing is
// staged on the run-scoped form.
func TestCommitCommandWithNothingThisRunWrote(t *testing.T) {
	changesRepo(t)
	m := newTestTUI()
	m.runStart = time.Now().Add(-time.Minute)

	m.handleCommand("/commit feat: nothing")

	if !strings.Contains(lastMessage(m), "Nothing this run's tool calls wrote") {
		t.Fatalf("reply = %q", lastMessage(m))
	}
	if m.dialogOnDone != nil {
		t.Fatal("a dialog opened for an empty change set")
	}
}

// gitLines runs one git command in repo and returns its output.
func gitLines(t *testing.T, repo string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", repo}, args...)...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// pushRepo is changesRepo plus a bare remote and a feature branch to push.
func pushRepo(t *testing.T) (repo, bare string) {
	t.Helper()
	repo = changesRepo(t)
	bare = t.TempDir()
	if out, err := exec.Command("git", "init", "--bare", bare).CombinedOutput(); err != nil {
		t.Fatalf("bare: %v\n%s", err, out)
	}
	gitLines(t, repo, "remote", "add", "origin", bare)
	gitLines(t, repo, "checkout", "-b", "feat/loop")
	if err := os.WriteFile(filepath.Join(repo, "feat.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitLines(t, repo, "add", "-A")
	gitLines(t, repo, "commit", "-m", "feat")
	return repo, bare
}

// TestPushCommandAsksThenPushes: Cancel publishes nothing, Push lands the branch.
func TestPushCommandAsksThenPushes(t *testing.T) {
	_, bare := pushRepo(t)
	m := newTestTUI()

	m.handleCommand("/push")
	if m.dialogOnDone == nil || !strings.Contains(m.dialogMsg, "Push feat/loop to origin?") {
		t.Fatalf("dialog = %q", m.dialogMsg)
	}
	m.dialogOnDone("Cancel")
	if _, err := os.Stat(filepath.Join(bare, "refs", "heads", "feat", "loop")); err == nil {
		t.Fatal("Cancel pushed the branch")
	}

	m.handleCommand("/push")
	m.dialogOnDone("Push")
	if !strings.Contains(lastMessage(m), "Pushed feat/loop") {
		t.Fatalf("reply = %q", lastMessage(m))
	}
	if _, err := os.Stat(filepath.Join(bare, "refs", "heads", "feat", "loop")); err != nil {
		t.Fatalf("the branch did not land: %v", err)
	}
}

// TestPushCommandRefusesTheDefaultBranch: the refusal is visible before a dialog
// opens, so a user on the default branch is told why.
func TestPushCommandRefusesTheDefaultBranch(t *testing.T) {
	remote := t.TempDir()
	repo := changesRepo(t)
	if out, err := exec.Command("git", "init", "--bare", remote).CombinedOutput(); err != nil {
		t.Fatalf("bare: %v\n%s", err, out)
	}
	gitLines(t, repo, "remote", "add", "origin", remote)

	m := newTestTUI()
	m.handleCommand("/push")

	if !strings.Contains(lastMessage(m), "default branch") {
		t.Fatalf("reply = %q", lastMessage(m))
	}
	if m.dialogOnDone != nil {
		t.Fatal("a dialog opened for the default branch")
	}
}

// TestPushCommandRefusedDuringARun: a run is writing the tree.
func TestPushCommandRefusedDuringARun(t *testing.T) {
	pushRepo(t)
	m := newTestTUI()
	m.runMu.Lock()
	m.runActive = true
	m.runMu.Unlock()
	t.Cleanup(func() {
		m.runMu.Lock()
		m.runActive = false
		m.runMu.Unlock()
	})

	m.handleCommand("/push")

	if !strings.Contains(m.statusMsg, "a run is in progress") {
		t.Fatalf("status = %q", m.statusMsg)
	}
}

// TestPullRequestCommandNeedsATitle: the title is the only thing the user must
// supply, so its absence is answered with usage, not a dialog.
func TestPullRequestCommandNeedsATitle(t *testing.T) {
	pushRepo(t)
	m := newTestTUI()

	m.handleCommand("/pr")

	if !strings.Contains(lastMessage(m), "usage: /pr <title>") {
		t.Fatalf("reply = %q", lastMessage(m))
	}
	if m.dialogOnDone != nil {
		t.Fatal("a dialog opened without a title")
	}
}

// TestPullRequestCommandNamesTheBranchAndBase: the dialog says what will be
// created and where, before anything leaves the machine.
func TestPullRequestCommandNamesTheBranchAndBase(t *testing.T) {
	pushRepo(t)
	m := newTestTUI()

	m.handleCommand("/pr feat: the loop")

	if m.dialogOnDone == nil || !strings.Contains(m.dialogMsg, "from feat/loop into") {
		t.Fatalf("dialog = %q", m.dialogMsg)
	}
	m.dialogOnDone("Cancel")
	if !strings.Contains(lastMessage(m), "cancelled") {
		t.Fatalf("reply = %q", lastMessage(m))
	}
}

// TestPullRequestCommandRefusesTheDefaultBranch.
func TestPullRequestCommandRefusesTheDefaultBranch(t *testing.T) {
	repo := changesRepo(t)
	m := newTestTUI()
	_ = repo

	m.handleCommand("/pr feat: x")

	if !strings.Contains(lastMessage(m), "default branch") {
		t.Fatalf("reply = %q", lastMessage(m))
	}
	if m.dialogOnDone != nil {
		t.Fatal("a dialog opened for the default branch")
	}
}
