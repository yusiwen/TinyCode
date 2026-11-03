package tool

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// changesRepo prepares a git repository with one commit and returns its root.
func changesRepo(t *testing.T) (root string) {
	t.Helper()
	project, _ := checkpointTestSetup(t)
	initGitRepo(t, project)
	writeFile(t, filepath.Join(project, "base.txt"), "base\n")
	gitCommit(t, project, "init")
	t.Chdir(project)
	return project
}

// toolWrite writes through the real tool, which is what records a checkpoint:
// calling the sandboxed write directly would bypass the fence's path resolution.
func toolWrite(t *testing.T, call int, path, content string) {
	t.Helper()
	ctx := types.WithToolCallScope(context.Background(), types.ToolCallScope{
		Tool: "write_file", Session: "s1", Run: 1, Step: 1, Call: call,
	})
	if _, err := WriteFile().Execute(ctx, map[string]any{"path": path, "content": content}); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestRunChangesAttributesThisRunsWrites is S1's core: a write that went through
// a tool is attributed to the run, and a hand edit is not.
func TestRunChangesAttributesThisRunsWrites(t *testing.T) {
	root := changesRepo(t)

	toolWrite(t, 1, filepath.Join(root, "written.go"), "one\ntwo\n")
	writeFile(t, filepath.Join(root, "hand.txt"), "hand\n")

	changes, err := RunChanges(context.Background(), "s1", time.Time{})
	if err != nil {
		t.Fatalf("RunChanges: %v", err)
	}
	run := changes.Run()
	if len(run) != 1 || run[0].Path != "written.go" {
		t.Fatalf("run group = %+v, want written.go", run)
	}
	if run[0].Added != 2 || run[0].Removed != 0 {
		t.Fatalf("written.go counts = +%d/-%d, want +2/-0", run[0].Added, run[0].Removed)
	}
	other := changes.Other()
	if len(other) != 1 || other[0].Path != "hand.txt" {
		t.Fatalf("other group = %+v, want hand.txt", other)
	}
	if other[0].Added != 1 {
		t.Fatalf("hand.txt added = %d, want 1", other[0].Added)
	}
}

// TestRunChangesRespectsTheTimeWindow proves the attribution uses the window:
// a call older than the run is not this run's work.
func TestRunChangesRespectsTheTimeWindow(t *testing.T) {
	root := changesRepo(t)
	toolWrite(t, 1, filepath.Join(root, "old.go"), "x\n")

	changes, err := RunChanges(context.Background(), "s1", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("RunChanges: %v", err)
	}
	if len(changes.Run()) != 0 {
		t.Fatalf("a call older than the window was attributed: %+v", changes.Run())
	}
	if len(changes.Other()) != 1 {
		t.Fatalf("other group = %+v, want the old write", changes.Other())
	}
}

// TestRunChangesRespectsTheSession: another session's write is not this one's.
func TestRunChangesRespectsTheSession(t *testing.T) {
	root := changesRepo(t)
	toolWrite(t, 1, filepath.Join(root, "mine.go"), "x\n")

	changes, err := RunChanges(context.Background(), "s2", time.Time{})
	if err != nil {
		t.Fatalf("RunChanges: %v", err)
	}
	if len(changes.Run()) != 0 {
		t.Fatalf("another session's write was attributed: %+v", changes.Run())
	}
}

// TestRunChangesReportsTheWholeTree: a clean tree reports nothing, and an undone
// write stops being attributed.
func TestRunChangesReportsTheWholeTree(t *testing.T) {
	root := changesRepo(t)

	changes, err := RunChanges(context.Background(), "s1", time.Time{})
	if err != nil {
		t.Fatalf("RunChanges: %v", err)
	}
	if !changes.Empty() {
		t.Fatalf("a clean tree reported %+v", changes.Files)
	}
	if got := changes.Report(); got != "No changes in the working tree." {
		t.Fatalf("clean report = %q", got)
	}

	toolWrite(t, 1, filepath.Join(root, "gone.go"), "x\n")
	call, ok, err := LastCall("s1")
	if err != nil || !ok {
		t.Fatalf("LastCall: %v ok=%v", err, ok)
	}
	if _, err := RestoreCall(context.Background(), call); err != nil {
		t.Fatalf("restore: %v", err)
	}
	changes, err = RunChanges(context.Background(), "s1", time.Time{})
	if err != nil {
		t.Fatalf("RunChanges: %v", err)
	}
	if !changes.Empty() || len(changes.Run()) != 0 {
		t.Fatalf("an undone write was still reported: %+v", changes.Files)
	}
}

// TestRunChangesReportNamesBothGroups pins the report's text: the reader has to
// be able to tell "this run" from "other" without reading the code.
func TestRunChangesReportNamesBothGroups(t *testing.T) {
	root := changesRepo(t)
	toolWrite(t, 1, filepath.Join(root, "a.go"), "1\n2\n3\n")
	writeFile(t, filepath.Join(root, "b.txt"), "b\n")

	changes, err := RunChanges(context.Background(), "s1", time.Time{})
	if err != nil {
		t.Fatalf("RunChanges: %v", err)
	}
	report := changes.Report()
	for _, want := range []string{
		"2 file(s) changed, +4/-0",
		"1 written by this run's tool calls, 1 other",
		"a.go",
		"this run",
		"b.txt",
		"other",
		"A shell command can write files the checkpoint does not see",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report is missing %q:\n%s", want, report)
		}
	}
}

// TestRunChangesOutsideARepository: the error names the situation instead of
// returning git's exit code.
func TestRunChangesOutsideARepository(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := RunChanges(context.Background(), "", time.Time{}); err == nil ||
		!strings.Contains(err.Error(), "not inside a git repository") {
		t.Fatalf("err = %v", err)
	}
}
