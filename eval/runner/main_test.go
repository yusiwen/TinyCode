package main

import (
	"os"
	"path/filepath"
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

// TestLoadTasksRefusesAMalformedTaskSet: a task without a test or a prompt would
// silently score as unsolvable, which reads as an agent failure.
func TestLoadTasksRefusesAMalformedTaskSet(t *testing.T) {
	noTests := t.TempDir()
	write(t, filepath.Join(noTests, "a", "go.mod"), "module task.local/a\n\ngo 1.27\n")
	write(t, filepath.Join(noTests, "a", "PROMPT"), "x\n")
	write(t, filepath.Join(noTests, "a", "a.go"), "package task\n")
	if _, err := loadTasks(noTests); err == nil {
		t.Fatal("a task with no test file was accepted")
	}

	noPrompt := t.TempDir()
	write(t, filepath.Join(noPrompt, "a", "go.mod"), "module task.local/a\n\ngo 1.27\n")
	write(t, filepath.Join(noPrompt, "a", "a_test.go"), "package task\n")
	if _, err := loadTasks(noPrompt); err == nil {
		t.Fatal("a task with no prompt was accepted")
	}

	ok := t.TempDir()
	write(t, filepath.Join(ok, "a", "go.mod"), "module task.local/a\n\ngo 1.27\n")
	write(t, filepath.Join(ok, "a", "PROMPT"), "make it pass\n")
	write(t, filepath.Join(ok, "a", "a_test.go"), "package task\n")
	tasks, err := loadTasks(ok)
	if err != nil {
		t.Fatalf("a well-formed task was refused: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Prompt != "make it pass" {
		t.Fatalf("tasks = %+v", tasks)
	}
}
