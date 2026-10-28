package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSpikeTasksAreSolvable proves both halves of every spike task the way
// TestEveryTaskIsSolvable does for the visible suite: red with the hidden suite
// installed, green after the reference solution. A task that cannot be solved
// measures the task, not the agent.
func TestSpikeTasksAreSolvable(t *testing.T) {
	tasks, err := loadSpikeTasks("../../eval/spike-repo-index/tasks", "../../eval/spike-repo-index/hidden")
	if err != nil {
		t.Fatalf("load spike tasks: %v", err)
	}
	if len(tasks) == 0 {
		t.Fatal("no spike tasks")
	}
	for _, task := range tasks {
		task := task
		t.Run(task.Name, func(t *testing.T) {
			work := t.TempDir()
			if err := copyTask(task, work); err != nil {
				t.Fatal(err)
			}
			// The work copy carries no test at all, which is the point: the
			// agent must find the defect from the prompt.
			if matches, _ := filepath.Glob(filepath.Join(work, "*_test.go")); len(matches) != 0 {
				t.Fatalf("the work copy already carries a test: %v", matches)
			}
			if err := installHiddenTests("../../eval/spike-repo-index/hidden", task.Name, work); err != nil {
				t.Fatal(err)
			}
			if gradeTask(work) {
				t.Fatalf("task %s passes before any fix, so it measures nothing", task.Name)
			}

			solutionDir := filepath.Join("../../eval/_solutions", task.Name)
			entries, err := os.ReadDir(solutionDir)
			if err != nil {
				t.Fatalf("task %s has no reference solution: %v", task.Name, err)
			}
			applied := 0
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(solutionDir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(work, entry.Name()), data, 0o644); err != nil {
					t.Fatal(err)
				}
				applied++
			}
			if applied == 0 {
				t.Fatalf("the reference solution for %s is empty", task.Name)
			}
			if !gradeTask(work) {
				t.Fatalf("task %s is still red after its reference solution", task.Name)
			}
		})
	}
}

// TestSpikePromptsDoNotNameFiles keeps the corpus honest: a prompt that names a
// file hands the agent the answer to the question the spike asks. Naming the
// API is allowed and necessary — the assertions are hidden.
func TestSpikePromptsDoNotNameFiles(t *testing.T) {
	tasks, err := loadSpikeTasks("../../eval/spike-repo-index/tasks", "../../eval/spike-repo-index/hidden")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		entries, err := os.ReadDir(task.Dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".go") {
				continue
			}
			if strings.Contains(task.Prompt, entry.Name()) {
				t.Errorf("task %s's prompt names the file %q", task.Name, entry.Name())
			}
		}
		for _, leak := range []string{"internal/", "internal ", "package "} {
			if strings.Contains(task.Prompt, leak) {
				t.Errorf("task %s's prompt contains %q", task.Name, leak)
			}
		}
	}
}

// TestRepoMapDigest pins the map's shape and its budget: files sorted, exported
// declarations with signatures, and the whole thing bounded.
func TestRepoMapDigest(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("store.go", `package task

import "fmt"

func NewStore() *Store { return &Store{} }

type Store struct{}

func (s *Store) Put(k string, v int) error { _ = fmt.Sprint(k); return nil }

func hidden() {}
`)
	write("internal/util/util.go", `package util

// Exported is part of the map; the unexported one is not.
func Exported(n int) string { return "" }

func unexported() {}
`)

	digest, err := repoMapDigest(dir, mapBudgetTokens)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	for _, want := range []string{
		"store.go (package task)",
		"func NewStore() *Store",
		"type Store struct",
		"func (s *Store) Put(k string, v int) error",
		"internal/util/util.go (package util)",
		"func Exported(n int) string",
		"imports: fmt",
	} {
		if !strings.Contains(digest, want) {
			t.Errorf("the map is missing %q:\n%s", want, digest)
		}
	}
	for _, unwanted := range []string{"hidden()", "unexported()"} {
		if strings.Contains(digest, unwanted) {
			t.Errorf("the map lists %q, which is not exported surface", unwanted)
		}
	}
	// Deterministic: two walks of the same tree say the same thing.
	again, err := repoMapDigest(dir, mapBudgetTokens)
	if err != nil || again != digest {
		t.Fatalf("the map is not deterministic (err=%v)", err)
	}

	// Bounded: a tiny budget stops the walk instead of returning everything.
	small, err := repoMapDigest(dir, 1)
	if err != nil {
		t.Fatalf("small budget: %v", err)
	}
	if len(small) >= len(digest) {
		t.Errorf("a one-token budget produced %d bytes, not fewer than %d", len(small), len(digest))
	}
}

// TestInstallHiddenTestsRefusesAnEmptySuite pins the vacuous-pass guard.
func TestInstallHiddenTestsRefusesAnEmptySuite(t *testing.T) {
	hidden := t.TempDir()
	if err := os.MkdirAll(filepath.Join(hidden, "task"), 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := installHiddenTests(hidden, "task", work); err == nil {
		t.Fatal("an empty hidden suite was accepted, so grading would pass without assertions")
	}
}

// TestSpikeTaskWorkCopiesCarryThePadding pins that the noise reaches the work
// copy: a task whose subpackages were dropped measures one file, not a tree.
func TestSpikeTaskWorkCopiesCarryThePadding(t *testing.T) {
	tasks, err := loadSpikeTasks("../../eval/spike-repo-index/tasks", "../../eval/spike-repo-index/hidden")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		work := t.TempDir()
		if err := copyTask(task, work); err != nil {
			t.Fatal(err)
		}
		packages, err := filepath.Glob(filepath.Join(work, "internal", "*", "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		if len(packages) < 8 {
			t.Errorf("task %s reached the work copy with %d padding files, want the tree", task.Name, len(packages))
		}
	}
}

// TestPromptForPutsTheMapOnlyInTheMapArm pins the treatment: the baseline
// prompt is untouched, and the map arm's prompt carries the digest and the task.
func TestPromptForPutsTheMapOnlyInTheMapArm(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package task\n\nfunc Exported() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	task := task{Name: "a", Dir: dir, Prompt: "Fix the behaviour."}

	plain := promptFor(configuration{Name: "build"}, task, dir)
	if plain != task.Prompt {
		t.Fatalf("the baseline prompt was altered: %q", plain)
	}

	mapped := promptFor(configuration{Name: "map", RepoMap: true}, task, dir)
	if !strings.Contains(mapped, "func Exported()") {
		t.Fatalf("the map arm's prompt is missing the digest:\n%s", mapped)
	}
	if !strings.HasSuffix(mapped, task.Prompt) {
		t.Fatalf("the map arm's prompt does not end with the task: %q", mapped)
	}
}
