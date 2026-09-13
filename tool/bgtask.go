package tool

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/tlog"
)

var nextTaskID atomic.Int64

// bgTaskTimeout bounds a background sub-agent run. It matches the synchronous
// task tool so both paths terminate abandoned sub-agents after the same period.
// It is a variable so tests can shorten it; production uses the 120s default.
var bgTaskTimeout = 120 * time.Second

// TaskState represents the state of a background task.
type TaskState int

const (
	TaskRunning TaskState = iota
	TaskDone
	TaskFailed
	TaskTimedOut
)

// BgTask holds the status and result of a background task.
// Its fields are guarded by the owning manager's mutex.
type BgTask struct {
	ID      string
	Agent   string
	Goal    string
	State   TaskState
	Result  string
	Error   string
	Done    chan struct{} // closed when task completes
	started time.Time

	doneOnce sync.Once // ensures Done is closed exactly once
}

// BackgroundTaskManager tracks all running background tasks.
type BackgroundTaskManager struct {
	mu    sync.Mutex
	tasks map[string]*BgTask
}

// NewBackgroundTaskManager creates a new task manager.
func NewBackgroundTaskManager() *BackgroundTaskManager {
	return &BackgroundTaskManager{
		tasks: make(map[string]*BgTask),
	}
}

// Start launches a background task and returns immediately with a task ID.
func (mgr *BackgroundTaskManager) Start(deps *TaskToolDeps, name, goal string) string {
	id := fmt.Sprintf("task_%d", nextTaskID.Add(1))

	task := &BgTask{
		ID:      id,
		Agent:   name,
		Goal:    goal,
		State:   TaskRunning,
		Done:    make(chan struct{}),
		started: time.Now(),
	}

	mgr.mu.Lock()
	mgr.tasks[id] = task
	mgr.mu.Unlock()

	go func() {
		// Look up sub-agent config
		cfg := deps.GetAgentConfig(name)
		if cfg == nil {
			mgr.finalize(task, TaskFailed, fmt.Sprintf("unknown agent %q", name), "")
			return
		}

		// Filter tools by sub-agent permissions
		var subTools []agent.Tool
		for _, t := range deps.AllTools {
			if cfg.IsToolAllowed(t.Name) {
				subTools = append(subTools, t)
			}
		}

		// Create sub-agent
		sub := agent.New(deps.Provider)
		sub.Config = cfg
		sub.Tools = subTools
		sub.MaxSteps = cfg.MaxSteps
		sub.ShowThinking = false
		sub.SessionStore = nil

		tlog.Debug("task.bg", "start", "id", id, "agent", name, "goal", goal,
			"tools", len(subTools), "maxSteps", cfg.MaxSteps)

		// Run with a cancellable context so the sub-agent is stopped at the
		// deadline even if it ignores ctx and blocks. The watchdog settles the
		// task state and releases Collect waiters at the hard timeout; the
		// once-guarded completion path below is idempotent with it.
		ctx, cancel := context.WithTimeout(context.Background(), bgTaskTimeout)
		defer cancel()

		watchdog := time.AfterFunc(bgTaskTimeout, func() {
			cancel()
			mgr.finalize(task, TaskTimedOut, "timed out after 120s", "")
		})
		defer watchdog.Stop()

		out, err := sub.Run(ctx, goal)

		if err != nil {
			tlog.Debug("task.bg", "error", "id", id, "err", err)
			if ctx.Err() != nil {
				result := ""
				if out != "" {
					result = fmt.Sprintf("[task %q timed out — partial result]\n%s", name, out)
				}
				mgr.finalize(task, TaskTimedOut, "timed out after 120s", result)
			} else if stringsContains(err.Error(), "max steps") && out != "" {
				mgr.finalize(task, TaskDone, err.Error(),
					fmt.Sprintf("[task %q hit max steps — partial result]\n%s", name, out))
			} else {
				mgr.finalize(task, TaskFailed, err.Error(), "")
			}
			return
		}

		tlog.Debug("task.bg", "done", "id", id, "output_size", len(out))
		mgr.finalize(task, TaskDone, "", out)
	}()

	return id
}

// finalize records a terminal state and releases Collect waiters exactly once.
// A task that already reached a terminal state (e.g. via the watchdog) is left
// untouched, so a late-completing sub-agent cannot overwrite it.
func (mgr *BackgroundTaskManager) finalize(task *BgTask, state TaskState, errMsg, result string) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if task.State == TaskRunning {
		task.State = state
		task.Error = errMsg
		task.Result = result
	}
	task.doneOnce.Do(func() { close(task.Done) })
}

// Collect waits for a background task to complete and returns its result.
// Returns an error if the task ID is unknown.
func (mgr *BackgroundTaskManager) Collect(taskID string) (string, error) {
	return mgr.CollectContext(context.Background(), taskID)
}

// CollectContext waits for a background task to complete and returns its result,
// returning early when ctx is done. The background task itself keeps running.
// Returns an error if the task ID is unknown.
func (mgr *BackgroundTaskManager) CollectContext(ctx context.Context, taskID string) (string, error) {
	mgr.mu.Lock()
	task, ok := mgr.tasks[taskID]
	mgr.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("task_collect: unknown task %q", taskID)
	}

	// Wait for completion or caller cancellation. Done is closed by finalize,
	// which is also driven by the watchdog, so this always returns within the
	// background task's hard deadline.
	select {
	case <-task.Done:
	case <-ctx.Done():
		return "", fmt.Errorf("task_collect: %w", ctx.Err())
	}

	mgr.mu.Lock()
	state, result, errMsg := task.State, task.Result, task.Error
	mgr.mu.Unlock()

	switch state {
	case TaskDone:
		return result, nil
	case TaskTimedOut:
		if result != "" {
			return result, nil
		}
		return "", fmt.Errorf("task_collect: task %q timed out", taskID)
	case TaskFailed:
		if result != "" {
			return result, nil
		}
		return "", fmt.Errorf("task_collect: task %q failed: %s", taskID, errMsg)
	default:
		return "", fmt.Errorf("task_collect: task %q in unexpected state %v", taskID, state)
	}
}

// Status returns the current state of a task, or empty string if unknown.
func (mgr *BackgroundTaskManager) Status(taskID string) string {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	task, ok := mgr.tasks[taskID]
	if !ok {
		return ""
	}
	elapsed := time.Since(task.started).Round(time.Second)
	switch task.State {
	case TaskRunning:
		return fmt.Sprintf("running (%s)", elapsed)
	case TaskDone:
		return fmt.Sprintf("done (%s)", elapsed)
	case TaskFailed:
		return "failed"
	case TaskTimedOut:
		return "timeout"
	}
	return ""
}

// stringsContains is a small helper to avoid importing strings in a hot path.
func stringsContains(s, substr string) bool {
	return len(s) >= len(substr) && containsString(s, substr)
}

func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
