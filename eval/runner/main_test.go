package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write makes a file with its directories.
func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGradeTaskRunsTheTasksOwnCommand pins the grading rule the spike rests on:
// the verdict is the task's own `go test ./...`, not anything the runner or a
// judge decides.
func TestGradeTaskRunsTheTasksOwnCommand(t *testing.T) {
	passing := t.TempDir()
	write(t, filepath.Join(passing, "go.mod"), "module task.local/ok\n\ngo 1.27\n")
	write(t, filepath.Join(passing, "ok_test.go"), "package task\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n")
	if !gradeTask(passing) {
		t.Fatal("a passing task was graded as a failure")
	}

	failing := t.TempDir()
	write(t, filepath.Join(failing, "go.mod"), "module task.local/bad\n\ngo 1.27\n")
	write(t, filepath.Join(failing, "bad_test.go"), "package task\n\nimport \"testing\"\n\nfunc TestBad(t *testing.T) { t.Fatal(\"no\") }\n")
	if gradeTask(failing) {
		t.Fatal("a failing task was graded as a pass")
	}
}

// TestTestsModifiedSeesAnEditedOrDeletedTest is the integrity rule: a pass
// obtained by weakening the test is not a pass, so the detection has to notice
// both an edit and a deletion.
func TestTestsModifiedSeesAnEditedOrDeletedTest(t *testing.T) {
	source := t.TempDir()
	write(t, filepath.Join(source, "go.mod"), "module task.local/x\n\ngo 1.27\n")
	testBody := "package task\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n"
	write(t, filepath.Join(source, "x_test.go"), testBody)

	digests, err := testDigests(source)
	if err != nil {
		t.Fatal(err)
	}
	tk := task{Name: "x", Dir: source, Tests: digests}

	work := t.TempDir()
	write(t, filepath.Join(work, "x_test.go"), testBody)
	if modified, err := testsModified(tk, work); err != nil || modified {
		t.Fatalf("an untouched test was reported modified: %v %v", modified, err)
	}

	write(t, filepath.Join(work, "x_test.go"), testBody+"// weakened\n")
	if modified, _ := testsModified(tk, work); !modified {
		t.Fatal("an edited test was not reported")
	}

	if err := os.Remove(filepath.Join(work, "x_test.go")); err != nil {
		t.Fatal(err)
	}
	if modified, _ := testsModified(tk, work); !modified {
		t.Fatal("a deleted test was not reported")
	}
}

// TestCopyTaskBringsTheSourcesAndNotThePrompt keeps the work copy to what the
// agent should see: PROMPT is the runner's input, not part of the task.
func TestCopyTaskBringsTheSourcesAndNotThePrompt(t *testing.T) {
	source := t.TempDir()
	write(t, filepath.Join(source, "go.mod"), "module task.local/x\n\ngo 1.27\n")
	write(t, filepath.Join(source, "x.go"), "package task\n")
	write(t, filepath.Join(source, "x_test.go"), "package task\n")
	write(t, filepath.Join(source, "PROMPT"), "do the thing\n")

	tk := task{Name: "x", Dir: source, Prompt: "do the thing"}
	work := t.TempDir()
	if err := copyTask(tk, work); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"go.mod", "x.go", "x_test.go"} {
		if _, err := os.Stat(filepath.Join(work, name)); err != nil {
			t.Errorf("%s was not copied: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "PROMPT")); !os.IsNotExist(err) {
		t.Errorf("PROMPT was copied into the work directory: %v", err)
	}
}

// TestLoadTasksRefusesAMalformedTaskSet: every piece a task needs is required,
// because a missing one would silently score as unsolvable and read as an agent
// failure. The tier is required too, and has to be a declared one.
func TestLoadTasksRefusesAMalformedTaskSet(t *testing.T) {
	wellFormed := func(t *testing.T, mutate func(dir string)) string {
		t.Helper()
		root := t.TempDir()
		task := filepath.Join(root, "a")
		write(t, filepath.Join(task, "go.mod"), "module task.local/a\n\ngo 1.27\n")
		write(t, filepath.Join(task, "PROMPT"), "make it pass\n")
		write(t, filepath.Join(task, "TIER"), "1\n")
		write(t, filepath.Join(task, "a_test.go"), "package task\n")
		if mutate != nil {
			mutate(task)
		}
		return root
	}

	cases := []struct {
		name   string
		mutate func(dir string)
	}{
		{"no test file", func(dir string) {
			if err := os.Remove(filepath.Join(dir, "a_test.go")); err != nil {
				t.Fatal(err)
			}
		}},
		{"no prompt", func(dir string) {
			if err := os.Remove(filepath.Join(dir, "PROMPT")); err != nil {
				t.Fatal(err)
			}
		}},
		{"no tier", func(dir string) {
			if err := os.Remove(filepath.Join(dir, "TIER")); err != nil {
				t.Fatal(err)
			}
		}},
		{"a tier outside the declared ones", func(dir string) {
			write(t, filepath.Join(dir, "TIER"), "7\n")
		}},
		{"a tier that is not a number", func(dir string) {
			write(t, filepath.Join(dir, "TIER"), "hard\n")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadTasks(wellFormed(t, tc.mutate)); err == nil {
				t.Fatalf("a task set with %s was accepted", tc.name)
			}
		})
	}

	t.Run("a well-formed task loads with its tier", func(t *testing.T) {
		tasks, err := loadTasks(wellFormed(t, nil))
		if err != nil {
			t.Fatalf("a well-formed task was refused: %v", err)
		}
		if len(tasks) != 1 || tasks[0].Prompt != "make it pass" || tasks[0].Tier != 1 {
			t.Fatalf("tasks = %+v", tasks)
		}
	})
}

// TestEveryTaskIsSolvable is the gate the task set needs before it can measure
// anything: a task nobody can solve measures the task, not the agent.
//
// For every task it proves both halves — the suite is red before the fix and
// green after it — with the reference fix kept in eval/_solutions, which is
// outside the task directory on purpose: a solution shipped inside a task would
// be copied into the work directory and handed to the agent.
func TestEveryTaskIsSolvable(t *testing.T) {
	tasks, err := loadTasks("../../eval/tasks")
	if err != nil {
		t.Fatalf("load tasks: %v", err)
	}
	tiers := make(map[int]int)
	for _, task := range tasks {
		// A copy, so the subtest does not depend on the loop variable.
		task := task
		tiers[task.Tier]++
		t.Run(task.Name, func(t *testing.T) {
			solutionDir := filepath.Join("../../eval/_solutions", task.Name)
			entries, err := os.ReadDir(solutionDir)
			if err != nil {
				t.Fatalf("task %s has no reference solution: %v", task.Name, err)
			}

			work := t.TempDir()
			if err := copyTask(task, work); err != nil {
				t.Fatal(err)
			}
			if gradeTask(work) {
				t.Fatalf("task %s passes before any fix, so it measures nothing", task.Name)
			}

			applied := 0
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
					continue
				}
				if strings.HasSuffix(entry.Name(), "_test.go") {
					t.Fatalf("the reference solution for %s contains a test file", task.Name)
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
				t.Fatalf("the reference solution for %s does not make it pass", task.Name)
			}
			modified, err := testsModified(task, work)
			if err != nil {
				t.Fatal(err)
			}
			if modified {
				t.Fatalf("the reference solution for %s edits a test file", task.Name)
			}
		})
	}

	// The gradient this increment is for: every declared tier has tasks, and the
	// set has all three.
	for tier := 1; tier <= 3; tier++ {
		if tiers[tier] == 0 {
			t.Errorf("tier %d has no tasks, so no gradient can be read", tier)
		}
	}
	if len(tasks) < 12 {
		t.Errorf("the set has %d tasks; the gradient increment was for 12", len(tasks))
	}
}

// TestEverySolutionBelongsToATask: an orphaned solution is a stale answer to a
// question nobody asks, and it would hide a task that lost its fix.
func TestEverySolutionBelongsToATask(t *testing.T) {
	tasks, err := loadTasks("../../eval/tasks")
	if err != nil {
		t.Fatal(err)
	}
	known := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		known[task.Name] = true
	}
	entries, err := os.ReadDir("../../eval/_solutions")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !known[entry.Name()] {
			t.Errorf("eval/_solutions/%s has no task", entry.Name())
		}
	}
}
