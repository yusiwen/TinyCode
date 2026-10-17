package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// A command changes files through whatever the shell runs, so the only way to
// have its pre-images is to capture them before it starts. This file is that
// capture: the project tree, read and hashed before the command, then diffed
// against after it.
//
// Why the bytes are copied rather than hard-linked (the cheaper trick): a hard
// link shares the inode, so `echo x > file` — a truncate in place, the ordinary
// way a shell writes a file — changes the link's content too, and the "pre-image"
// would be the new bytes. A wrong pre-image is worse than a missing one, because
// an undo would silently apply it. The measured cost of the honest version is in
// docs/checkpoints.md.
// The bounds are variables rather than constants so a test can lower them and
// exercise the skip path without building a tree of the real size.
var (
	// checkpointSnapshotMaxFileBytes is the per-file cap. A file above it is not
	// captured, and the tool result says how many were skipped: a binary or a
	// build artefact is not what a command's undo is for, and reading it would
	// cost more than the rest of the tree together.
	checkpointSnapshotMaxFileBytes = int64(2 << 20)
	// The whole-snapshot caps, chosen from the measurement in docs/checkpoints.md
	// rather than guessed: this repository's 375 files under the per-file cap are
	// 2.4 MiB and take about a tenth of a second to read and hash, so 32 MiB /
	// 4000 files leaves roughly an order of magnitude of headroom before a
	// command's snapshot is skipped (and said to be skipped).
	checkpointSnapshotMaxBytes = int64(32 << 20)
	checkpointSnapshotMaxFiles = 4000
)

// snapshotFile is one file's pre-command state. The size, mtime and inode are the
// change detector: after the command a path is a candidate if any of them moved,
// which is a stat call rather than a second read of the tree. A same-size write
// that also restores the mtime would fool it; a shell command doing that is a
// deliberate act, not the ordinary way a command edits a file, and the limit is
// stated in the document rather than left to be discovered.
type snapshotFile struct {
	sha   string
	mode  os.FileMode
	size  int64
	mtime time.Time
	ino   uint64
}

// treeSnapshot is the project tree as it was before a command ran.
type treeSnapshot struct {
	root  string
	store string
	files map[string]snapshotFile
	// uncaptured names the paths that existed but were not read (over the
	// per-file cap, or unreadable). They are kept apart from "not in the
	// snapshot" because the two mean opposite things to a diff: an uncaptured
	// path was there before the command and must be left alone, while a path the
	// snapshot never saw is one the command created.
	uncaptured map[string]bool
	oversize   int    // files above the per-file cap: not captured
	irregular  int    // symlinks, devices and other non-regular files: not captured
	skipped    string // non-empty when the whole snapshot was skipped, and why
}

// beginCommandSnapshot captures the project tree for the command about to run.
//
// It returns nil when there is nothing to record against — the caller is not the
// agent loop, or the store cannot be resolved — and a snapshot whose `skipped`
// field is set when the tree is over its bound. Failing the command instead is
// deliberately not what this does: the command's purpose is whatever the model
// asked for, and a record that cannot be made is a fact to report, not a reason
// to refuse work. That is the opposite of the file tools, whose whole purpose is
// the write they must be able to undo.
func beginCommandSnapshot(ctx context.Context) *treeSnapshot {
	if _, ok := types.ToolCallScopeFrom(ctx); !ok {
		return nil
	}
	store, err := checkpointStoreDir()
	if err != nil {
		tlog.Warn("checkpoint", "snapshot_unavailable", "err", err)
		return nil
	}
	root := DefaultSandbox.projectRootResolved()
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil
		}
		root = wd
	}

	snap := &treeSnapshot{root: root, store: store, files: map[string]snapshotFile{}, uncaptured: map[string]bool{}}
	var total int64
	err = walkProjectTree(root, store, func(path string, info fs.FileInfo) error {
		if info.Size() > checkpointSnapshotMaxFileBytes {
			snap.oversize++
			snap.uncaptured[path] = true
			return nil
		}
		total += info.Size()
		if total > checkpointSnapshotMaxBytes || len(snap.files)+1 > checkpointSnapshotMaxFiles {
			snap.skipped = fmt.Sprintf("the project tree is over the snapshot bound (%d files, %s); this command's changes are not recorded",
				len(snap.files), sizeLabel(checkpointSnapshotMaxBytes))
			return fs.SkipAll
		}
		data, err := readSandboxed(path)
		if err != nil {
			// A file that cannot be read is not a reason to lose the rest of the
			// snapshot; it is one more thing the record does not cover.
			tlog.Debug("checkpoint", "snapshot_unreadable", "path", path, "err", err)
			snap.irregular++
			snap.uncaptured[path] = true
			return nil
		}
		sum := sha256.Sum256(data)
		snap.files[path] = snapshotFile{
			sha:   hex.EncodeToString(sum[:]),
			mode:  info.Mode().Perm(),
			size:  info.Size(),
			mtime: info.ModTime(),
			ino:   inodeOf(info),
		}
		if err := writeObject(store, snap.files[path].sha, data); err != nil {
			return fmt.Errorf("store %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		tlog.Warn("checkpoint", "snapshot_failed", "root", root, "err", err)
		snap.skipped = fmt.Sprintf("the project tree could not be read (%v); this command's changes are not recorded", err)
		snap.files = map[string]snapshotFile{}
	}
	return snap
}

// note is the line a tool result carries about this snapshot: empty when
// everything was captured, so a fully recorded command says nothing extra.
func (s *treeSnapshot) note() string {
	if s == nil {
		return ""
	}
	var parts []string
	if s.skipped != "" {
		parts = append(parts, s.skipped)
	}
	if s.oversize > 0 {
		parts = append(parts, fmt.Sprintf("%d file(s) over %s were not captured",
			s.oversize, sizeLabel(checkpointSnapshotMaxFileBytes)))
	}
	if s.irregular > 0 {
		parts = append(parts, fmt.Sprintf("%d path(s) could not be read", s.irregular))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[checkpoint: " + strings.Join(parts, "; ") + "]"
}

// finish records the command's effects. It runs on every exit path — success,
// failure, timeout and cancellation — through a defer, because the changes a
// killed command already made are the ones an undo is most likely to be wanted
// for. Failures here are logged and never change the command's result: the
// command has already run, and its output is what the caller asked for.
func (s *treeSnapshot) finish(ctx context.Context) {
	if s == nil || s.skipped != "" {
		return
	}
	scope, ok := types.ToolCallScopeFrom(ctx)
	if !ok {
		return
	}
	entries, err := s.changes()
	if err != nil {
		tlog.Warn("checkpoint", "snapshot_diff_failed", "err", err)
		return
	}
	if len(entries) == 0 {
		// A command that changed nothing writes no manifest and leaves nothing
		// behind: the store only ever holds what an undo may need.
		return
	}
	if err := appendManifest(s.store, scope, entries...); err != nil {
		tlog.Warn("checkpoint", "snapshot_manifest_failed", "err", err)
		return
	}
	if dropped, err := pruneCheckpoints(s.store); err != nil {
		tlog.Warn("checkpoint", "prune_failed", "store", s.store, "err", err)
	} else if dropped > 0 {
		tlog.Info("checkpoint", "pruned", "store", s.store, "calls", dropped)
	}
	tlog.Info("checkpoint", "command_recorded", "tool", scope.Tool, "files", len(entries))
}

// changes diffs the tree against the snapshot. Only a stat is needed per path:
// the record's payload is the pre-image, which is already stored, and the current
// bytes are not part of it.
func (s *treeSnapshot) changes() ([]CheckpointEntry, error) {
	seen := map[string]bool{}
	var entries []CheckpointEntry
	err := walkProjectTree(s.root, s.store, func(path string, info fs.FileInfo) error {
		seen[path] = true
		if s.uncaptured[path] {
			// It was there before the command and its bytes were never read, so
			// neither its content nor its existence can be restored. Recording it
			// as new would make an undo delete a file the command merely edited;
			// the note already counts it as not captured.
			return nil
		}
		pre, existed := s.files[path]
		if !existed {
			// A path the snapshot never saw: the undo removes it. This works for
			// any size, so a new file is recorded even above the per-file cap.
			entries = append(entries, CheckpointEntry{Path: path, Existed: false})
			return nil
		}
		if pre.size == info.Size() && pre.mtime.Equal(info.ModTime()) && pre.ino == inodeOf(info) {
			return nil
		}
		entries = append(entries, CheckpointEntry{Path: path, Existed: true, SHA256: pre.sha, Mode: pre.mode})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for path, pre := range s.files {
		if seen[path] {
			continue
		}
		// Gone: the undo recreates it from the pre-image.
		entries = append(entries, CheckpointEntry{Path: path, Existed: true, SHA256: pre.sha, Mode: pre.mode})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

// sizeLabel renders a byte count the way the note and the bounds read in prose.
func sizeLabel(bytes int64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%d MiB", bytes>>20)
	case bytes >= 1<<10:
		return fmt.Sprintf("%d KiB", bytes>>10)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// walkProjectTree visits the project root's regular files. It skips the git
// directory — a command that commits should not cost a snapshot of the object
// database, and git has its own recovery — and the checkpoint store itself, which
// is never a tool path and never something an undo restores.
func walkProjectTree(root, store string, fn func(path string, info fs.FileInfo) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable directory is skipped, not fatal
		}
		if d.IsDir() {
			if path == root {
				return nil
			}
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			if store != "" && beneathRoot(store, path) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		return fn(path, info)
	})
}
