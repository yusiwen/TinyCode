package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// runCommand drives the real bash tool with a checkpoint scope, which is what
// makes it snapshot the tree first.
func runCommand(t *testing.T, ctx context.Context, args map[string]any) string {
	t.Helper()
	if _, ok := args["workdir"]; !ok {
		t.Fatal("the test must pass workdir: the tool otherwise runs in the process cwd")
	}
	result, err := Bash().Execute(ctx, args)
	if err != nil {
		t.Fatalf("bash: %v", err)
	}
	return result
}

// TestCommandSnapshotRecordsWhatTheCommandChanged is S1's end-to-end gate for
// issue #238: a command edits, deletes and creates files, and /undo's store gets
// one manifest that restores all three — the deletion recreated, the edit
// returned to its previous bytes, the creation removed.
//
// The edit is a truncate in place (`printf > file`), which is the shape that ruled
// out a hard-link snapshot: a link shares the inode, so its content would be the
// *new* bytes and the "pre-image" would be a lie. This test is what would break if
// someone replaced the copy with the link.
func TestCommandSnapshotRecordsWhatTheCommandChanged(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	ctx := callScope(context.Background(), "bash", 1, 0)

	modified := filepath.Join(project, "modified.txt")
	deleted := filepath.Join(project, "deleted.txt")
	created := filepath.Join(project, "created.txt")
	writeFileForTest(t, modified, "before\n", 0600)
	writeFileForTest(t, deleted, "gone soon\n", 0640)
	beforeModified, beforeDeleted := shaOf(t, modified), shaOf(t, deleted)

	runCommand(t, ctx, map[string]any{
		"workdir": project,
		"command": "printf 'after\\n' > modified.txt && rm -f deleted.txt && printf 'new\\n' > created.txt",
	})

	if shaOf(t, modified) == beforeModified {
		t.Fatal("the command did not change the file")
	}
	if _, err := os.Stat(deleted); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the command did not delete the file: %v", err)
	}

	call, ok, err := LastCall("")
	if err != nil || !ok {
		t.Fatalf("last call: %v, %v", ok, err)
	}
	if call.Tool != "bash" {
		t.Errorf("the manifest is attributed to %q, want bash", call.Tool)
	}
	paths := call.Paths()
	if len(paths) != 3 {
		t.Fatalf("%d entries, want 3 (modify, delete, create): %v", len(paths), paths)
	}

	if _, err := RestoreCall(ctx, call); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := shaOf(t, modified); got != beforeModified {
		t.Errorf("modified.txt is %s, want its pre-command bytes %s", got, beforeModified)
	}
	if info, err := os.Stat(modified); err != nil || info.Mode().Perm() != 0600 {
		t.Errorf("modified.txt mode: info=%v err=%v, want 0600", info, err)
	}
	if got := shaOf(t, deleted); got != beforeDeleted {
		t.Errorf("deleted.txt is %s, want the bytes the command removed %s", got, beforeDeleted)
	}
	if info, err := os.Stat(deleted); err != nil || info.Mode().Perm() != 0640 {
		t.Errorf("deleted.txt mode: info=%v err=%v, want 0640", info, err)
	}
	if _, err := os.Stat(created); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the created file survived the undo: %v", err)
	}
}

// TestCommandThatChangesNothingWritesNoManifest covers the other half of the
// promise: a read-only command costs no record, so the store holds only what an
// undo may need.
func TestCommandThatChangesNothingWritesNoManifest(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	ctx := callScope(context.Background(), "bash", 1, 0)
	writeFileForTest(t, filepath.Join(project, "readme.txt"), "hello\n", 0644)

	result := runCommand(t, ctx, map[string]any{"workdir": project, "command": "cat readme.txt hello.txt 2>/dev/null; true"})
	if strings.Contains(result, "[checkpoint:") {
		t.Errorf("a fully recorded command must not add a note, got %q", result)
	}
	calls, err := ListCalls(0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("%d manifests for a command that changed nothing: %+v", len(calls), calls)
	}
}

// TestCommandSnapshotSkippedOverTheBoundSaysSo pins the fact that makes an
// unprotected command honest rather than silent: over the bound the snapshot is
// skipped, the command still runs, and its result says the changes were not
// recorded.
func TestCommandSnapshotSkippedOverTheBoundSaysSo(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	previousFiles := checkpointSnapshotMaxFiles
	checkpointSnapshotMaxFiles = 2
	t.Cleanup(func() { checkpointSnapshotMaxFiles = previousFiles })

	ctx := callScope(context.Background(), "bash", 1, 0)
	for i := 0; i < 5; i++ {
		writeFileForTest(t, filepath.Join(project, fmt.Sprintf("f%d.txt", i)), "x\n", 0644)
	}

	result := runCommand(t, ctx, map[string]any{
		"workdir": project,
		"command": "printf 'written\\n' > f0.txt",
	})
	if !strings.Contains(result, "[checkpoint:") || !strings.Contains(result, "not recorded") {
		t.Errorf("the result must say the changes were not recorded, got %q", result)
	}
	if data, _ := os.ReadFile(filepath.Join(project, "f0.txt")); string(data) != "written\n" {
		t.Error("the command must still run when its record cannot be made")
	}
	if calls, _ := ListCalls(0); len(calls) != 0 {
		t.Errorf("%d manifests written by a skipped snapshot", len(calls))
	}
}

// TestCommandSnapshotReportsFilesOverThePerFileCap is the per-file bound: a file
// above it is not read, and the tool result counts it, so the gap is visible.
func TestCommandSnapshotReportsFilesOverThePerFileCap(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	previousCap := checkpointSnapshotMaxFileBytes
	checkpointSnapshotMaxFileBytes = 8
	t.Cleanup(func() { checkpointSnapshotMaxFileBytes = previousCap })

	ctx := callScope(context.Background(), "bash", 1, 0)
	writeFileForTest(t, filepath.Join(project, "big.txt"), "0123456789abcdef\n", 0644)

	result := runCommand(t, ctx, map[string]any{
		"workdir": project,
		"command": "printf 'small\\n' >> big.txt",
	})
	if !strings.Contains(result, "were not captured") {
		t.Errorf("the result must count the file it could not capture, got %q", result)
	}
	call, ok, err := LastCall("")
	if err != nil {
		t.Fatalf("last call: %v", err)
	}
	if ok {
		t.Errorf("an oversize modified file must not be recorded, got %+v", call.Entries)
	}
}

// TestCommandSnapshotRecordsAFileItCreatedAboveTheCap is the nuance that makes the
// cap safe: a *new* file needs no bytes to undo it — the undo removes it — so its
// size is irrelevant and it is recorded even when the per-file cap would have
// skipped reading it.
func TestCommandSnapshotRecordsAFileItCreatedAboveTheCap(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	previousCap := checkpointSnapshotMaxFileBytes
	checkpointSnapshotMaxFileBytes = 4
	t.Cleanup(func() { checkpointSnapshotMaxFileBytes = previousCap })

	ctx := callScope(context.Background(), "bash", 1, 0)
	runCommand(t, ctx, map[string]any{
		"workdir": project,
		"command": "printf 'a much longer new file\\n' > new.txt",
	})

	call, ok, err := LastCall("")
	if err != nil || !ok {
		t.Fatalf("last call: %v, %v", ok, err)
	}
	if len(call.Entries) != 1 || call.Entries[0].Existed {
		t.Fatalf("entries = %+v, want one record that the file did not exist", call.Entries)
	}
	if _, err := RestoreCall(ctx, call); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "new.txt")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the undo must remove the file the command created: %v", err)
	}
}

// TestCommandKilledByItsTimeoutStillRecordsWhatItChanged is the exit path that
// matters most: a command killed at its deadline has already changed files, and
// those changes are exactly the ones a reader may want back.
func TestCommandKilledByItsTimeoutStillRecordsWhatItChanged(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	ctx := callScope(context.Background(), "bash", 1, 0)
	existing := filepath.Join(project, "existing.txt")
	writeFileForTest(t, existing, "before\n", 0644)
	before := shaOf(t, existing)

	result := runCommand(t, ctx, map[string]any{
		"workdir": project,
		"timeout": float64(1),
		"command": "printf 'during\\n' > existing.txt && sleep 30",
	})
	if !strings.Contains(result, "ERROR:") {
		t.Errorf("the command should have been killed at its deadline, got %q", result)
	}

	call, ok, err := LastCall("")
	if err != nil || !ok {
		t.Fatalf("a killed command's changes must still be recorded: ok=%v err=%v", ok, err)
	}
	if _, err := RestoreCall(ctx, call); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got := shaOf(t, existing); got != before {
		t.Errorf("existing.txt is %s, want the bytes it had before the killed command (%s)", got, before)
	}
}

// BenchmarkCommandSnapshot measures what the snapshot costs on this repository,
// which is the number the bounds were chosen from (docs/checkpoints.md). Run it
// with: go test ./tool -run XXX -bench CommandSnapshot -benchtime 5x
func BenchmarkCommandSnapshot(b *testing.B) {
	root, err := filepath.Abs("..")
	if err != nil {
		b.Fatal(err)
	}
	previousRoot := DefaultSandbox.ProjectRoot
	DefaultSandbox.ProjectRoot = root
	previousStore := CheckpointRoot()
	SetCheckpointRoot(b.TempDir())
	b.Cleanup(func() {
		DefaultSandbox.ProjectRoot = previousRoot
		SetCheckpointRoot(previousStore)
	})

	ctx := types.WithToolCallScope(context.Background(), types.ToolCallScope{Tool: "bash", Run: 1, Step: 1})
	b.ResetTimer()
	var total int64
	for i := 0; i < b.N; i++ {
		snap := beginCommandSnapshot(ctx)
		if snap == nil {
			b.Fatal("no snapshot")
		}
		if snap.skipped != "" {
			b.Fatalf("the snapshot was skipped: %s", snap.skipped)
		}
		total = int64(len(snap.files))
	}
	b.StopTimer()
	if total == 0 {
		b.Fatal("the snapshot is empty")
	}
	b.ReportMetric(float64(total), "files")
}

// TestSnapshotObjectsAreContentAddressed pins the property the byte bound relies
// on: two files with the same bytes cost one object, and a second snapshot of an
// unchanged tree writes nothing new.
func TestSnapshotObjectsAreContentAddressed(t *testing.T) {
	project, _ := checkpointTestSetup(t)
	writeFileForTest(t, filepath.Join(project, "a.txt"), "same\n", 0644)
	writeFileForTest(t, filepath.Join(project, "b.txt"), "same\n", 0644)
	ctx := callScope(context.Background(), "bash", 1, 0)

	storeDir, err := checkpointStoreDir()
	if err != nil {
		t.Fatalf("store dir: %v", err)
	}

	runCommand(t, ctx, map[string]any{"workdir": project, "command": "true"})
	first := countObjects(t)
	if first == 0 {
		t.Fatal("the snapshot stored no object")
	}
	runCommand(t, scopeForCall(ctx, 2), map[string]any{"workdir": project, "command": "true"})
	if got := countObjects(t); got != first {
		t.Errorf("a second snapshot added %d objects for an unchanged tree", got-first)
	}

	sum := sha256.Sum256([]byte("same\n"))
	if _, err := os.Stat(objectPath(storeDir, hex.EncodeToString(sum[:]))); err != nil {
		t.Errorf("identical bytes are not one object: %v", err)
	}
}

// scopeForCall keeps the run and session of ctx but moves to another call index.
func scopeForCall(ctx context.Context, call int) context.Context {
	scope, _ := types.ToolCallScopeFrom(ctx)
	scope.Call = call
	return types.WithToolCallScope(ctx, scope)
}

// countObjects counts the pre-images in the project's store.
func countObjects(t *testing.T) int {
	t.Helper()
	store, err := checkpointStoreDir()
	if err != nil {
		t.Fatalf("store dir: %v", err)
	}
	n := 0
	_ = filepath.WalkDir(filepath.Join(store, checkpointObjectsDir), func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}
