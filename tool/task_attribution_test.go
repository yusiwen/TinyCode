package tool

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// scopedCtx is a tool-call scope with a task label.
func scopedCtx(run int, label string, step, call int) context.Context {
	return types.WithToolCallScope(context.Background(), types.ToolCallScope{
		Tool: "write_file", Run: run, Step: step, Call: call, Task: label,
	})
}

// TestSubagentWritesNameTheirTask: the manifest records the label, and the
// parent's own writes keep it empty.
func TestSubagentWritesNameTheirTask(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	writeFileForTest(t, project+"/parent.txt", "p\n", 0o644)
	writeFileForTest(t, project+"/child.txt", "c\n", 0o644)

	if _, err := WriteFile().Execute(scopedCtx(1, "", 1, 0), map[string]any{"path": project + "/parent.txt", "content": "p2\n"}); err != nil {
		t.Fatalf("parent write: %v", err)
	}
	if _, err := WriteFile().Execute(scopedCtx(1, "general#7", 1, 0), map[string]any{"path": project + "/child.txt", "content": "c2\n"}); err != nil {
		t.Fatalf("child write: %v", err)
	}

	calls, err := ListCalls(0)
	if err != nil {
		t.Fatalf("ListCalls: %v", err)
	}
	// The manifest stores the fence's resolved path, which on macOS turns
	// /var into /private/var: key by base name, which is what this test is about.
	tasks := map[string]string{}
	for _, call := range calls {
		for _, path := range call.Paths() {
			tasks[filepath.Base(path)] = call.Task
		}
	}
	if tasks["parent.txt"] != "" {
		t.Fatalf("the parent's write carries a task label: %+v", tasks)
	}
	if tasks["child.txt"] != "general#7" {
		t.Fatalf("the sub-agent's write lost its label: %+v", tasks)
	}
}

// TestParentAndSubagentManifestsDoNotMix: both run 1, both step 1 call 0 — two
// manifests, one entry each, because the label is part of the identity.
func TestParentAndSubagentManifestsDoNotMix(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	writeFileForTest(t, project+"/one.txt", "1\n", 0o644)
	writeFileForTest(t, project+"/two.txt", "2\n", 0o644)

	if _, err := WriteFile().Execute(scopedCtx(1, "", 1, 0), map[string]any{"path": project + "/one.txt", "content": "1b\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile().Execute(scopedCtx(1, "explore#2", 1, 0), map[string]any{"path": project + "/two.txt", "content": "2b\n"}); err != nil {
		t.Fatal(err)
	}

	calls, err := ListCalls(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("got %d manifests, want 2:\n%+v", len(calls), calls)
	}
	for _, call := range calls {
		if len(call.Entries) != 1 {
			t.Fatalf("manifest %s mixes %d entries", call.Task, len(call.Entries))
		}
	}
}

// TestNoteTaskWritesReportsTheSecondWriter: the registry is per parent run, and a
// path is reported by whoever arrives second.
func TestNoteTaskWritesReportsTheSecondWriter(t *testing.T) {
	run := "test-run-attribution" // the registry is process-wide: name it here
	other := "test-run-attribution-2"
	if got := NoteTaskWrites(run, "general#1", []string{"a.go", "b.go"}); len(got) != 0 {
		t.Fatalf("the first task reported a conflict: %v", got)
	}
	got := NoteTaskWrites(run, "general#2", []string{"b.go", "c.go"})
	if len(got) != 1 || got[0] != "b.go" {
		t.Fatalf("conflicts = %v, want [b.go]", got)
	}
	if got := NoteTaskWrites(other, "general#3", []string{"b.go"}); len(got) != 0 {
		t.Fatalf("another parent run reported a conflict: %v", got)
	}
}

// TestConflictNoteNamesBothTasks: the note a task reads says which files and that
// the later write won.
func TestConflictNoteNamesBothTasks(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	writeFileForTest(t, project+"/shared.go", "1\n", 0o644)

	first := scopedCtx(7, "general#1", 1, 0)
	if _, err := WriteFile().Execute(first, map[string]any{"path": project + "/shared.go", "content": "one\n"}); err != nil {
		t.Fatal(err)
	}
	if note := ConflictNote(first, "general#1"); note != "" {
		t.Fatalf("the first task reported a conflict: %q", note)
	}

	second := scopedCtx(7, "general#2", 1, 0)
	if _, err := WriteFile().Execute(second, map[string]any{"path": project + "/shared.go", "content": "two\n"}); err != nil {
		t.Fatal(err)
	}
	note := ConflictNote(second, "general#2")
	if !strings.Contains(note, "[conflict]") || !strings.Contains(note, "shared.go") {
		t.Fatalf("note = %q", note)
	}
	if !strings.Contains(note, "later write is what is on disk") {
		t.Fatalf("note does not say which one won: %q", note)
	}
}

// TestTaskPathsIsEmptyForAnUnknownLabel: a task that wrote nothing has no note.
func TestTaskPathsIsEmptyForAnUnknownLabel(t *testing.T) {
	checkpointTestSetup(t)
	if paths := TaskPaths("nobody#99"); len(paths) != 0 {
		t.Fatalf("paths = %v", paths)
	}
}
