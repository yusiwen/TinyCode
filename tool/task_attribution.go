package tool

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/yusiwen/tinycode/types"
)

// taskSeq numbers delegated tasks within this process, so two concurrent calls
// to the same sub-agent have distinct labels. The label is what a checkpoint
// manifest records and what a conflict report names (roadmap C3).
var taskSeq atomic.Int64

// TaskLabelFor names one delegated task: the sub-agent it used and a
// per-process number, e.g. "general#7".
func TaskLabelFor(agentName string) string {
	return fmt.Sprintf("%s#%d", agentName, taskSeq.Add(1))
}

// TaskPaths returns the repository paths the delegated task's tool calls wrote,
// from the checkpoint manifests that carry its label, sorted and deduplicated.
func TaskPaths(label string) []string {
	if label == "" {
		return nil
	}
	calls, err := ListCalls(0)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var paths []string
	for _, call := range calls {
		if call.Task != label {
			continue
		}
		for _, path := range call.Paths() {
			if !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	sort.Strings(paths)
	return paths
}

// taskWrites remembers, per parent run, which delegated task wrote which path.
//
// It is in memory on purpose: the question it answers ("did two tasks in this run
// write the same file?") is about the run that is happening, and a restart is not
// a run that has to be reconciled.
var taskWrites = struct {
	mu    sync.Mutex
	byRun map[int]map[string]string
}{byRun: map[int]map[string]string{}}

// NoteTaskWrites records one task's paths and returns those another task in the
// same parent run already wrote. A path is reported by whichever task arrives
// second: the later write is the one on disk, and the earlier agent's reasoning
// was about content that no longer exists (roadmap C3).
func NoteTaskWrites(parentRun int, label string, paths []string) []string {
	taskWrites.mu.Lock()
	defer taskWrites.mu.Unlock()
	if taskWrites.byRun == nil {
		taskWrites.byRun = map[int]map[string]string{}
	}
	seen := taskWrites.byRun[parentRun]
	if seen == nil {
		seen = map[string]string{}
		taskWrites.byRun[parentRun] = seen
	}
	var conflicts []string
	for _, path := range paths {
		if other, ok := seen[path]; ok && other != label {
			conflicts = append(conflicts, path)
			continue
		}
		seen[path] = label
	}
	return conflicts
}

// ConflictNote reports, for the task that just finished, the paths another task
// in the same parent run also wrote. It returns "" when there is nothing to say.
//
// The parent run comes from the context the task tool was called with: a
// delegated task inherits its parent's run number for this purpose, while its own
// calls carry its label so their manifests stay separate.
func ConflictNote(ctx context.Context, label string) string {
	paths := TaskPaths(label)
	if len(paths) == 0 {
		return ""
	}
	parentRun := 0
	if scope, ok := types.ToolCallScopeFrom(ctx); ok {
		parentRun = scope.Run
	}
	conflicts := NoteTaskWrites(parentRun, label, paths)
	if len(conflicts) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[conflict] another task in this run also wrote %d file(s):\n", len(conflicts))
	for _, path := range conflicts {
		fmt.Fprintf(&b, "  %s\n", path)
	}
	b.WriteString("The later write is what is on disk; the other task's version of those files is gone.")
	return b.String()
}
