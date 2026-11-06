package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRepoTaskIsRedBeforeAndGreenAfter proves the machinery end to end on one
// task: the base tree is materialized from git, the fix's tests are injected, the
// task is red, and the fix's own non-test diff makes it green.
//
// One task, not both: the second one's package takes half a minute to build, and
// this test runs three times in the repeated-run job. The machinery is the same
// for every task; the PR records the manual proof for both.
func TestRepoTaskIsRedBeforeAndGreenAfter(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := loadRepoTasks("../../eval/repo-tasks", repo)
	if err != nil {
		t.Fatalf("load repo tasks: %v", err)
	}
	var chosen task
	for _, tk := range tasks {
		if tk.Name == "hook-timeout" {
			chosen = tk
		}
	}
	if chosen.Repo == nil {
		t.Fatal("hook-timeout is not among the repo tasks")
	}

	work := t.TempDir()
	if err := materializeRepoTask(chosen.Repo, work); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	// The work copy must not carry the grade: that is what "hidden" means.
	for _, hidden := range chosen.Repo.Hidden {
		if data, err := os.ReadFile(filepath.Join(work, hidden)); err == nil &&
			strings.Contains(string(data), "WaitDelay") {
			t.Fatalf("the work copy already carries the fix's test: %s", hidden)
		}
	}
	if err := injectRepoTaskTests(chosen.Repo, work); err != nil {
		t.Fatalf("inject the grade: %v", err)
	}
	if gradeRepoTask(work, chosen.Repo.Packages) {
		t.Fatal("the task is green before the fix, so it measures nothing")
	}

	patch, err := repoSolutionPatch(chosen.Repo)
	if err != nil {
		t.Fatalf("solution patch: %v", err)
	}
	if strings.TrimSpace(patch) == "" {
		t.Fatal("the reference patch is empty")
	}
	apply := exec.Command("git", "apply", "-")
	apply.Dir = work
	apply.Stdin = strings.NewReader(patch)
	if out, err := apply.CombinedOutput(); err != nil {
		t.Fatalf("apply the reference patch: %v\n%s", err, out)
	}
	if !gradeRepoTask(work, chosen.Repo.Packages) {
		t.Fatal("the task is still red after the reference patch")
	}
}

// TestRepoTasksDeclareWhatTheyNeed: every task names a base, a fix and at least
// one package and hidden test — the loader refuses anything less, this pins the
// corpus as it stands.
func TestRepoTasksDeclareWhatTheyNeed(t *testing.T) {
	repo, _ := filepath.Abs("../..")
	tasks, err := loadRepoTasks("../../eval/repo-tasks", repo)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(tasks) < 2 {
		t.Fatalf("only %d repo tasks", len(tasks))
	}
	for _, tk := range tasks {
		if tk.Prompt == "" || tk.Repo.Base == "" || tk.Repo.Fix == "" {
			t.Fatalf("task %s is incomplete: %+v", tk.Name, tk.Repo)
		}
	}
}
