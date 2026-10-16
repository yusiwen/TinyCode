package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// checkpointTestSetup points the store at a throwaway directory and the fence at
// a throwaway project, and restores both when the test ends.
//
// The run table is reset as well: manifest names are allocated once per
// (store, process, run, step, call), so a leftover entry would hand a later test
// a manifest under an earlier test's store.
func checkpointTestSetup(t *testing.T) (project, store string) {
	t.Helper()
	project = t.TempDir()
	store = t.TempDir()

	checkpointMu.Lock()
	previousRoot, previousRuns := checkpointRoot, checkpointRuns
	checkpointRoot, checkpointRuns = store, map[string]*checkpointRun{}
	checkpointMu.Unlock()

	previousProject, previousAllowed := DefaultSandbox.ProjectRoot, DefaultSandbox.AutoAllowPaths
	DefaultSandbox.ProjectRoot = project

	t.Cleanup(func() {
		checkpointMu.Lock()
		checkpointRoot, checkpointRuns = previousRoot, previousRuns
		checkpointMu.Unlock()
		DefaultSandbox.ProjectRoot, DefaultSandbox.AutoAllowPaths = previousProject, previousAllowed
	})
	return project, store
}

// callScope is the context the agent loop stamps on one tool call.
func callScope(ctx context.Context, toolName string, step, call int) context.Context {
	return types.WithToolCallScope(ctx, types.ToolCallScope{
		Tool: toolName, Run: 1, Step: step, Call: call,
	})
}

func shaOf(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func writeFileForTest(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// TestCheckpointRestoresAModifyACreateAndADelete is S1's end-to-end gate: the
// three shapes a tool call can impose on a file — replace its bytes, create it,
// delete it — each recorded, and each returned byte for byte to its pre-call
// state, mode included.
func TestCheckpointRestoresAModifyACreateAndADelete(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	ctx := context.Background()

	modified := filepath.Join(project, "modified.txt")
	created := filepath.Join(project, "created.txt")
	doomed := filepath.Join(project, "doomed.txt")
	writeFileForTest(t, modified, "before\n", 0600)
	writeFileForTest(t, doomed, "goodbye\n", 0640)
	beforeModified, beforeDoomed := shaOf(t, modified), shaOf(t, doomed)

	edits, _ := json.Marshal([]editOp{{OldString: "before", NewString: "after"}})
	if _, err := Edit().Execute(callScope(ctx, "edit", 1, 0), map[string]any{
		"path": modified, "edits": json.RawMessage(edits),
	}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if _, err := WriteFile().Execute(callScope(ctx, "write_file", 1, 1), map[string]any{
		"path": created, "content": "new\n",
	}); err != nil {
		t.Fatalf("write_file: %v", err)
	}
	if _, err := ApplyPatch().Execute(callScope(ctx, "apply_patch", 1, 2), map[string]any{
		"patch_text": "*** Begin Patch\n*** Delete File: " + doomed + "\n*** End Patch\n",
	}); err != nil {
		t.Fatalf("apply_patch: %v", err)
	}

	// The calls really did change the files, so a passing restore cannot be a
	// restore of something that never moved.
	if shaOf(t, modified) == beforeModified {
		t.Fatal("edit did not change the file")
	}
	if _, err := os.Stat(created); err != nil {
		t.Fatalf("write_file did not create the file: %v", err)
	}
	if _, err := os.Stat(doomed); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("apply_patch did not delete the file: %v", err)
	}

	calls, err := ListCalls(0)
	if err != nil {
		t.Fatalf("list calls: %v", err)
	}
	if len(calls) != 3 {
		t.Fatalf("recorded %d calls, want 3: %+v", len(calls), calls)
	}
	for i, want := range []string{"edit", "write_file", "apply_patch"} {
		if calls[i].Tool != want {
			t.Errorf("call %d is %q, want %q (the manifests must sort in call order)", i, calls[i].Tool, want)
		}
	}
	if got := calls[0].Entries; len(got) != 1 || !got[0].Existed || got[0].SHA256 != beforeModified {
		t.Errorf("the edit's manifest = %+v, want one existing entry holding the pre-edit bytes", got)
	}
	if got := calls[1].Entries; len(got) != 1 || got[0].Existed {
		// A created file records that it did not exist, which is what makes the
		// restore remove it.
		t.Errorf("the write_file manifest = %+v, want one entry with existed=false", got)
	}
	if got := calls[2].Entries; len(got) != 1 || !got[0].Existed || got[0].SHA256 != beforeDoomed {
		t.Errorf("the apply_patch manifest = %+v, want one existing entry holding the deleted bytes", got)
	}

	// Newest first, which is the order /undo walks them in.
	for i := len(calls) - 1; i >= 0; i-- {
		restored, err := RestoreCall(ctx, calls[i])
		if err != nil {
			t.Fatalf("restore %s: %v", calls[i].Tool, err)
		}
		if len(restored) != 1 {
			t.Errorf("restore %s touched %d paths, want 1: %v", calls[i].Tool, len(restored), restored)
		}
	}

	if got := shaOf(t, modified); got != beforeModified {
		t.Errorf("modified.txt is %s, want its pre-edit bytes %s", got, beforeModified)
	}
	if info, err := os.Stat(modified); err != nil {
		t.Errorf("modified.txt is unreadable after the restore: %v", err)
	} else if info.Mode().Perm() != 0600 {
		t.Errorf("modified.txt mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(created); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the created file survived the restore: %v", err)
	}
	if got := shaOf(t, doomed); got != beforeDoomed {
		t.Errorf("doomed.txt is %s, want its deleted bytes %s", got, beforeDoomed)
	}
	if info, err := os.Stat(doomed); err != nil {
		t.Errorf("doomed.txt is unreadable after the restore: %v", err)
	} else if info.Mode().Perm() != 0640 {
		t.Errorf("doomed.txt mode = %v, want 0640", info.Mode().Perm())
	}
}

// TestCheckpointRecordsNothingWithoutACallScope pins the escape hatch that keeps
// every existing caller unchanged: outside the agent loop — a test, a library
// use — a write records nothing and creates no store.
func TestCheckpointRecordsNothingWithoutACallScope(t *testing.T) {
	project, store := checkpointTestSetup(t)
	// The sandbox-aware open requires the resolved form (macOS resolves its temp
	// directory through /var → /private/var), which is what CheckPathAccess hands
	// the tools.
	path := DefaultSandbox.ResolvePath(filepath.Join(project, "plain.txt"))

	if err := writeSandboxed(context.Background(), path, []byte("plain\n"), 0644); err != nil {
		t.Fatalf("write without a scope: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the write itself must still happen: %v", err)
	}
	calls, err := ListCalls(0)
	if err != nil {
		t.Fatalf("list calls: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("recorded %d calls without a scope, want 0", len(calls))
	}
	if _, err := os.Stat(filepath.Join(store, checkpointCallsDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a write without a scope created a store: %v", err)
	}
}

// TestCheckpointRefusesAWriteWhosePreImageCannotBeRead is the fail-closed gate.
// A directory is the injection: opening it succeeds, reading it fails with
// EISDIR, and that is neither "the file does not exist" nor something a restore
// could work from. The write must be refused, and the directory left alone.
func TestCheckpointRefusesAWriteWhosePreImageCannotBeRead(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	dir := filepath.Join(project, "a-directory")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	_, err := WriteFile().Execute(callScope(context.Background(), "write_file", 1, 0), map[string]any{
		"path": dir, "content": "this must not land\n",
	})
	if err == nil {
		t.Fatal("a write whose pre-image cannot be read must be refused, not performed unundoably")
	}
	if !strings.Contains(err.Error(), "pre-image cannot be read") {
		t.Errorf("the refusal must say why: %v", err)
	}
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		t.Errorf("the target must be untouched: info=%v err=%v", info, statErr)
	}
}

// TestCheckpointStoreIsNotAToolPath pins that the recovery store is outside the
// model's reach even where the fence would allow it — a user's "always allow" on
// the directory around the store must not open it. Full access is the documented
// exception: that mode means no boundary at all.
func TestCheckpointStoreIsNotAToolPath(t *testing.T) {
	_, store := checkpointTestSetup(t)
	DefaultSandbox.ProjectRoot = filepath.Dir(store)
	target := filepath.Join(store, checkpointObjectsDir, "aa", "deadbeef")

	_, denied, err := CheckPathAccess(context.Background(), target)
	if err != nil {
		t.Fatalf("check path: %v", err)
	}
	if denied == "" {
		t.Fatal("a tool path beneath the checkpoint store must be refused")
	}
	for _, want := range []string{types.RefusalMarker, "checkpoint store", "ask: " + types.AskReserved} {
		if !strings.Contains(denied, want) {
			t.Errorf("the refusal is missing %q:\n%s", want, denied)
		}
	}

	full := types.WithSandboxPolicy(context.Background(), types.SandboxPolicy{
		Mode: types.SandboxFullAccess, ProjectRoot: filepath.Dir(store),
	})
	if _, fullDenied, err := CheckPathAccess(full, target); err != nil || fullDenied != "" {
		t.Errorf("full access must not fence the store: denied=%q err=%v", fullDenied, err)
	}
}

// TestLastCallWalksBackwardsAsCallsAreUndone pins the S2 property that makes a
// second undo useful: an undone call is marked in its own manifest, so the next
// undo reaches the call before it rather than restoring the same bytes again.
// The manifests stay on disk — they are the record that an undo happened.
func TestLastCallWalksBackwardsAsCallsAreUndone(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	ctx := context.Background()
	target := DefaultSandbox.ResolvePath(filepath.Join(project, "walk.txt"))
	writeFileForTest(t, target, "v0\n", 0644)

	for i := 0; i < 3; i++ {
		if err := writeSandboxed(callScope(ctx, "write_file", 1, i), target, []byte(fmt.Sprintf("v%d\n", i+1)), 0644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}

	for want := 2; want >= 0; want-- {
		call, ok, err := LastCall("")
		if err != nil {
			t.Fatalf("last call: %v", err)
		}
		if !ok {
			t.Fatalf("no call to undo while call %d is still un-undone", want)
		}
		if call.Call != want {
			t.Fatalf("last call is %d, want %d (the newest un-undone call)", call.Call, want)
		}
		if _, err := RestoreCall(ctx, call); err != nil {
			t.Fatalf("restore %d: %v", want, err)
		}
	}

	if _, ok, _ := LastCall(""); ok {
		t.Error("every call has been undone, so there is nothing left to undo")
	}
	if calls, _ := ListCalls(0); len(calls) != 3 {
		t.Errorf("%d manifests on disk, want 3: an undo marks a call, it does not delete the record", len(calls))
	}
	if got, _ := os.ReadFile(target); string(got) != "v0\n" {
		t.Errorf("after undoing every call the file is %q, want its state before the first one", got)
	}
}

// TestLastCallSkipsAnotherSessionsCalls covers the session filter: a call
// attributed to a different session is skipped, while a call with no session
// recorded stays a candidate for any session — it was written by a process that
// had not minted its session id yet, which is the normal case for the first tool
// call of a TUI run.
func TestLastCallSkipsAnotherSessionsCalls(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	ctx := context.Background()
	theirs := DefaultSandbox.ResolvePath(filepath.Join(project, "theirs.txt"))
	unnamed := DefaultSandbox.ResolvePath(filepath.Join(project, "unnamed.txt"))

	if err := writeSandboxed(types.WithToolCallScope(ctx, types.ToolCallScope{
		Tool: "write_file", Session: "session-b", Run: 1, Step: 1, Call: 0,
	}), theirs, []byte("theirs\n"), 0644); err != nil {
		t.Fatalf("write for session-b: %v", err)
	}
	if err := writeSandboxed(types.WithToolCallScope(ctx, types.ToolCallScope{
		Tool: "write_file", Run: 2, Step: 1, Call: 0,
	}), unnamed, []byte("unnamed\n"), 0644); err != nil {
		t.Fatalf("write without a session: %v", err)
	}

	call, ok, err := LastCall("session-a")
	if err != nil || !ok {
		t.Fatalf("session-a: last call = %+v, %v, %v (the unattributed call is a candidate)", call, ok, err)
	}
	if call.Session != "" {
		t.Errorf("session-a picked up %q, want the unattributed call", call.Session)
	}
	if _, err := RestoreCall(ctx, call); err != nil {
		t.Fatalf("restore the unattributed call: %v", err)
	}

	if _, ok, _ := LastCall("session-a"); ok {
		t.Error("session-a must not reach session-b's call")
	}
	call, ok, _ = LastCall("session-b")
	if !ok || call.Session != "session-b" {
		t.Errorf("session-b's own call is missing: %+v (ok=%v)", call, ok)
	}
	if _, ok, _ := LastCall(""); !ok {
		t.Error("the CLI's unscoped lookup must still find it")
	}
}

// TestRestoreReportsWhatItRestoredWhenItFailsPartway covers the failure shape a
// caller has to describe: the files already restored come back with the error,
// and the call stays un-undone so a retry is possible (the pre-images are the
// state before the call, not before the retry, so retrying is safe).
func TestRestoreReportsWhatItRestoredWhenItFailsPartway(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	ctx := context.Background()
	first := DefaultSandbox.ResolvePath(filepath.Join(project, "first.txt"))
	second := DefaultSandbox.ResolvePath(filepath.Join(project, "second.txt"))
	writeFileForTest(t, first, "first-before\n", 0644)
	writeFileForTest(t, second, "second-before\n", 0644)

	// One call, two writes: the second pre-image will be removed before the
	// restore, which is the one failure a restore cannot work around.
	callCtx := callScope(ctx, "apply_patch", 1, 0)
	if err := writeSandboxed(callCtx, first, []byte("first-after\n"), 0644); err != nil {
		t.Fatalf("write first: %v", err)
	}
	if err := writeSandboxed(callCtx, second, []byte("second-after\n"), 0644); err != nil {
		t.Fatalf("write second: %v", err)
	}

	call, ok, err := LastCall("")
	if err != nil || !ok {
		t.Fatalf("last call: %+v, %v", ok, err)
	}
	if len(call.Entries) != 2 {
		t.Fatalf("the call has %d entries, want 2", len(call.Entries))
	}
	store := filepath.Dir(filepath.Dir(call.File))
	if err := os.Remove(objectPath(store, call.Entries[1].SHA256)); err != nil {
		t.Fatalf("remove the second pre-image: %v", err)
	}

	restored, err := RestoreCall(ctx, call)
	if err == nil {
		t.Fatal("a restore with a missing pre-image must fail")
	}
	if len(restored) != 1 || restored[0] != first {
		t.Errorf("restored = %v, want exactly the first path", restored)
	}
	if got, _ := os.ReadFile(first); string(got) != "first-before\n" {
		t.Errorf("first.txt is %q, want its pre-call bytes", got)
	}
	if got, _ := os.ReadFile(second); string(got) != "second-after\n" {
		t.Errorf("second.txt is %q, want it untouched by the failed restore", got)
	}
	calls, _ := ListCalls(0)
	if len(calls) != 1 || calls[0].Restored() {
		t.Error("a partway restore must leave the call un-undone, so it can be retried")
	}
}
