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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// Checkpoints: the bytes a tool write replaces, kept so the write can be undone.
//
// The property this file exists for is a single sentence: every tool-mediated
// content write can be returned to its pre-write state, byte for byte. It is
// what makes the agent safe to hand a file to, and it is deliberately
// fail-closed — a write whose pre-image cannot be recorded is refused rather
// than performed unundoably.
//
// The store is one object per pre-image (content-addressed by SHA-256, so the
// same bytes are stored once) plus one manifest per tool call. It lives outside
// the project root (`~/.tinycode/checkpoints/<project key>/` by default), so the
// fence that confines every tool write confines the store too, and a `rm -rf`
// inside the workspace cannot reach it. CheckPathAccess refuses tool access to
// it even when the fence would otherwise allow the path.
//
// The known boundary, stated here rather than discovered later: a file changed
// by a `bash` command or by any other subprocess leaves no pre-image, because
// nothing tells this layer what such a command will touch. Feeding the same
// store from a workspace-level producer is a separate piece of work; the store's
// shape does not change for it.
const (
	checkpointObjectsDir = "objects"
	checkpointCallsDir   = "calls"
)

var (
	checkpointMu   sync.Mutex
	checkpointRoot string                        // explicit override; "" = the default under the user's home
	checkpointRuns = map[string]*checkpointRun{} // key: store + pid + run
)

// checkpointRun groups the calls of one run, so their manifests sort in call
// order and two runs in one process cannot share a step number.
type checkpointRun struct {
	prefix string            // the run's first write, in UnixNano, zero-padded
	calls  map[string]string // "step-call" -> manifest basename
	next   int
}

// SetCheckpointRoot points the store at dir. It is called once at startup from
// the configured `checkpoint_dir`; an empty dir keeps the default.
func SetCheckpointRoot(dir string) {
	checkpointMu.Lock()
	defer checkpointMu.Unlock()
	checkpointRoot = dir
}

// CheckpointRoot reports the directory the store lives in. An empty string means
// no home directory could be found, and recording is refused rather than
// silently skipped: a write nobody can undo is what this layer exists to
// prevent.
func CheckpointRoot() string {
	checkpointMu.Lock()
	root := checkpointRoot
	checkpointMu.Unlock()
	if root != "" {
		return root
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".tinycode", "checkpoints")
}

// CheckpointEntry is one file's state before a tool call wrote it.
type CheckpointEntry struct {
	Path string `json:"path"`
	// Existed is false for a file the call created: restoring it means removing
	// it, not writing empty bytes.
	Existed bool `json:"existed"`
	// SHA256 addresses the pre-image in the object store. Empty when the file
	// did not exist.
	SHA256 string `json:"sha256,omitempty"`
	// Mode is the permission the file had, so a restore does not widen or narrow
	// it.
	Mode os.FileMode `json:"mode,omitempty"`
}

// CheckpointCall is one tool call's recorded pre-images, oldest write first.
type CheckpointCall struct {
	// File is the manifest's path. It is not serialized: it is where the call
	// was found, not part of what it records.
	File string `json:"-"`
	// Tool, Session, Run, Step and Call are the attribution carried with the
	// call: the tool name and the per-call numbering come from the agent loop,
	// the session from whoever started the run when it knew one.
	Tool    string    `json:"tool"`
	Session string    `json:"session,omitempty"`
	Run     int       `json:"run"`
	Step    int       `json:"step"`
	Call    int       `json:"call"`
	Time    time.Time `json:"time"`
	// RestoredAt is when an undo returned this call's files to their pre-call
	// state. A non-zero value takes the call out of LastCall's reach, which is
	// what makes a second /undo reach the call before it rather than repeat this
	// one. The manifest stays: it is the record that the undo happened.
	RestoredAt time.Time `json:"restored_at,omitempty"`
	// Entries keeps the write order of the call. Restore reads the *first*
	// entry per path, which is the true pre-call state when one call wrote the
	// same file twice.
	Entries []CheckpointEntry `json:"entries"`
}

// Restored reports whether this call has already been undone.
func (c CheckpointCall) Restored() bool { return !c.RestoredAt.IsZero() }

// Paths lists the files the call wrote, in the order they were written — what an
// undo is about to change, for a caller that shows it before asking.
func (c CheckpointCall) Paths() []string {
	paths := make([]string, 0, len(c.Entries))
	for _, e := range c.Entries {
		paths = append(paths, e.Path)
	}
	return paths
}

// recordPreImage stores path's current bytes under the call recorded in ctx.
//
// It is called by writeSandboxed before the replacement, and every failure is
// returned to the caller as a refusal: the invariant is that a tool write is
// undoable, so a write whose pre-image cannot be recorded must not happen. A
// context with no tool-call scope records nothing — that is the path taken by
// tests and by any caller that is not the agent loop.
func recordPreImage(ctx context.Context, path string) error {
	scope, ok := types.ToolCallScopeFrom(ctx)
	if !ok {
		return nil
	}
	store, err := checkpointStoreDir()
	if err != nil {
		return fmt.Errorf("checkpoint %s: %w", path, err)
	}

	prev, readErr := readSandboxed(path)
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return fmt.Errorf("checkpoint %s: the pre-image cannot be read, so the write is refused: %w", path, readErr)
	}

	entry := CheckpointEntry{
		Path:    filepath.Clean(absoluteNoClean(path)),
		Existed: readErr == nil,
	}
	if entry.Existed {
		sum := sha256.Sum256(prev)
		entry.SHA256 = hex.EncodeToString(sum[:])
		entry.Mode = fileMode(path)
		if err := writeObject(store, entry.SHA256, prev); err != nil {
			return fmt.Errorf("checkpoint %s: %w", path, err)
		}
	}
	if err := appendManifest(store, scope, entry); err != nil {
		return fmt.Errorf("checkpoint %s: %w", path, err)
	}
	return nil
}

// checkpointStoreDir resolves the store for the project in flight. The key
// mixes the project's directory name (so a person can tell two of them apart)
// with a hash of its resolved path (so two projects that share a name do not
// share a store).
func checkpointStoreDir() (string, error) {
	root := CheckpointRoot()
	if root == "" {
		return "", errors.New("the checkpoint store has no directory: no home directory is available")
	}
	project := DefaultSandbox.projectRootResolved()
	if project == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("the checkpoint store cannot name the project: %w", err)
		}
		project = wd
	}
	sum := sha256.Sum256([]byte(project))
	key := sanitizeCheckpointName(filepath.Base(project)) + "-" + hex.EncodeToString(sum[:6])
	return filepath.Join(root, key), nil
}

// manifestFor returns the manifest path of the call the scope names, creating a
// run group on first use. The name sorts by run and then by call, which is what
// makes "the last tool call" a file listing rather than a guess.
func manifestFor(store string, scope types.ToolCallScope) string {
	runKey := fmt.Sprintf("%s|%d|%d", store, os.Getpid(), scope.Run)
	callKey := fmt.Sprintf("%d-%d", scope.Step, scope.Call)
	run, ok := checkpointRuns[runKey]
	if !ok {
		run = &checkpointRun{prefix: fmt.Sprintf("%020d", time.Now().UnixNano()), calls: map[string]string{}}
		checkpointRuns[runKey] = run
	}
	if name, ok := run.calls[callKey]; ok {
		return filepath.Join(store, checkpointCallsDir, name)
	}
	name := fmt.Sprintf("%s-%06d-%s-step%d-call%d.json",
		run.prefix, run.next, sanitizeCheckpointName(scope.Tool), scope.Step, scope.Call)
	run.next++
	run.calls[callKey] = name
	return filepath.Join(store, checkpointCallsDir, name)
}

// appendManifest adds one entry to the call's manifest, writing it atomically so
// a reader sees the manifest before or after the entry, never half of it.
func appendManifest(store string, scope types.ToolCallScope, entry CheckpointEntry) error {
	checkpointMu.Lock()
	manifest := manifestFor(store, scope)
	checkpointMu.Unlock()

	call := CheckpointCall{
		Tool: scope.Tool, Session: scope.Session,
		Run: scope.Run, Step: scope.Step, Call: scope.Call, Time: time.Now(),
	}
	if data, err := os.ReadFile(manifest); err == nil {
		if err := json.Unmarshal(data, &call); err != nil {
			return fmt.Errorf("manifest %s is unreadable: %w", manifest, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("manifest %s is unreadable: %w", manifest, err)
	}
	call.Entries = append(call.Entries, entry)
	return writeFileAtomic(manifest, mustJSON(call), 0644)
}

// ListCalls returns the recorded calls of this project, oldest first. A limit
// above zero keeps only the newest that many.
func ListCalls(limit int) ([]CheckpointCall, error) {
	store, err := checkpointStoreDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(store, checkpointCallsDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("checkpoint store %s is unreadable: %w", store, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if limit > 0 && len(names) > limit {
		names = names[len(names)-limit:]
	}
	calls := make([]CheckpointCall, 0, len(names))
	for _, name := range names {
		path := filepath.Join(store, checkpointCallsDir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("checkpoint manifest %s is unreadable: %w", path, err)
		}
		var call CheckpointCall
		if err := json.Unmarshal(data, &call); err != nil {
			return nil, fmt.Errorf("checkpoint manifest %s is unreadable: %w", path, err)
		}
		call.File = path
		calls = append(calls, call)
	}
	return calls, nil
}

// LastCall returns the newest call that has not been undone yet, so a second
// undo reaches the call before it instead of repeating the first. When a session
// is named, a call attributed to a *different* session is skipped; a call with no
// session recorded is still a candidate, because it was written by a process that
// had not named its session yet — the TUI mints its id on the first save, which
// happens after the first tool call it records.
func LastCall(session string) (CheckpointCall, bool, error) {
	calls, err := ListCalls(0)
	if err != nil {
		return CheckpointCall{}, false, err
	}
	for i := len(calls) - 1; i >= 0; i-- {
		call := calls[i]
		if call.Restored() {
			continue
		}
		if session != "" && call.Session != "" && call.Session != session {
			continue
		}
		return call, true, nil
	}
	return CheckpointCall{}, false, nil
}

// RestoreCall returns the files call wrote to their pre-call state and reports
// which paths it touched, in the order it touched them.
//
// A restore does not ask the permission dialog again: every path in the manifest
// was written by a tool call that had already passed the fence, and asking twice
// for the same path would make recovery depend on a dialog whose answer the
// reader cannot know. The write itself still goes through the sandbox-aware open
// (see writeFileThroughFence), so containment is enforced by the same call that
// produces the bytes.
//
// On success the call is marked undone in its own manifest, which is what keeps
// LastCall moving backwards. A restore that fails partway returns the paths
// already restored with the error, so a caller can say what changed rather than
// reporting a bare failure; the call is left unmarked, and a retry is safe
// because the pre-images are the state before the call, not before the retry.
func RestoreCall(ctx context.Context, call CheckpointCall) ([]string, error) {
	if call.File == "" {
		return nil, errors.New("checkpoint restore: the call has no manifest path")
	}
	store := filepath.Dir(filepath.Dir(call.File))

	first := map[string]CheckpointEntry{}
	order := make([]string, 0, len(call.Entries))
	for _, entry := range call.Entries {
		if _, seen := first[entry.Path]; seen {
			continue
		}
		first[entry.Path] = entry
		order = append(order, entry.Path)
	}

	restored := make([]string, 0, len(order))
	for _, path := range order {
		entry := first[path]
		if !entry.Existed {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return restored, fmt.Errorf("checkpoint restore %s: %w", path, err)
			}
			restored = append(restored, path)
			continue
		}
		data, err := os.ReadFile(objectPath(store, entry.SHA256))
		if err != nil {
			return restored, fmt.Errorf("checkpoint restore %s: the pre-image is missing: %w", path, err)
		}
		if err := writeFileThroughFence(path, data, entry.Mode.Perm()); err != nil {
			return restored, fmt.Errorf("checkpoint restore %s: %w", path, err)
		}
		restored = append(restored, path)
	}

	call.RestoredAt = time.Now()
	if err := writeFileAtomic(call.File, mustJSON(call), 0644); err != nil {
		// The files are back where they were; only the record of it failed. The
		// message says so, because a caller that reported a bare error would leave
		// the reader unsure whether anything changed.
		return restored, fmt.Errorf("checkpoint restore: the files were restored, but marking %s as undone failed: %w", call.File, err)
	}
	return restored, nil
}

// checkpointStoreRefusal refuses tool access to the store. It is consulted by
// CheckPathAccess before the fence decides, because the store usually sits
// outside the project root yet a user may have allowed the directory around it:
// an "always allow" on `~/.tinycode` must not open the recovery store.
//
// Full access is the one mode that bypasses this. That mode means no boundary at
// all — a deliberate choice by the user — and quietly fencing one path inside it
// would make the mode mean something narrower than it says.
func checkpointStoreRefusal(ctx context.Context, path string) string {
	store := CheckpointRoot()
	if store == "" {
		return ""
	}
	policy := runPolicy(ctx)
	if policy.Mode == types.SandboxFullAccess {
		return ""
	}
	realStore := filepath.Clean(resolveRealPath(absoluteNoClean(store)))
	if !beneathRoot(realStore, filepath.Clean(resolveRealPath(absoluteNoClean(path)))) {
		return ""
	}
	return types.Refusal{
		Subject: fmt.Sprintf("path %q", path),
		Reason:  "is inside the checkpoint store, which holds the bytes a tool write replaced",
		Mode:    policy.Mode,
		Ask:     types.AskReserved,
		Detail:  []string{"store: " + store},
	}.Message()
}

// objectPath shards objects by their first two hex characters: one directory per
// 256 objects keeps a directory listing cheap on a long-lived store.
func objectPath(store, sum string) string {
	if len(sum) < 2 {
		return filepath.Join(store, checkpointObjectsDir, sum)
	}
	return filepath.Join(store, checkpointObjectsDir, sum[:2], sum)
}

// writeObject stores one pre-image under its digest. The same bytes written
// twice are stored once.
func writeObject(store, sum string, data []byte) error {
	path := objectPath(store, sum)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	return writeFileAtomic(path, data, 0444)
}

// writeFileAtomic writes data to path through a temp file in the same directory
// and one rename, so a reader never sees a partial object or manifest. Content
// objects are written read-only: a pre-image is evidence, not something to edit.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("create a temp file in %s: %w", dir, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("chmod %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	committed = true
	return nil
}

// fileMode reports a file's permission bits, or 0644 when it cannot be read
// (which a successful pre-image read makes unlikely, and a wrong guess only
// affects the mode a restore recreates).
func fileMode(path string) os.FileMode {
	info, err := os.Stat(path)
	if err != nil {
		return 0644
	}
	return info.Mode().Perm()
}

// sanitizeCheckpointName keeps a project or tool name usable inside a file name
// without letting it escape the store directory.
func sanitizeCheckpointName(name string) string {
	if name == "" {
		return "unnamed"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" {
		return "unnamed"
	}
	return out
}

// mustJSON marshals a manifest, which is built from strings, ints and byte
// slices and therefore cannot fail. A panic here would be a programming error
// rather than an input problem, and it is contained by the agent loop, which
// turns a panicking tool into an error result.
func mustJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(fmt.Sprintf("checkpoint manifest: %v", err))
	}
	return data
}
