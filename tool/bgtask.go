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

// TaskState represents the state of a background task.
type TaskState int

const (
	TaskRunning TaskState = iota
	TaskDone
	TaskFailed
	TaskTimedOut
)

// BgTask holds the status and result of a background task.
type BgTask struct {
	ID       string
	Agent    string
	Goal     string
	State    TaskState
	Result   string
	Error    string
	Done     chan struct{} // closed when task completes
	started  time.Time
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
			task.State = TaskFailed
			task.Error = fmt.Sprintf("unknown agent %q", name)
			close(task.Done)
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

		// Run with context timeout
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		out, err := sub.Run(ctx, goal)

		mgr.mu.Lock()
		if err != nil {
			tlog.Debug("task.bg", "error", "id", id, "err", err)
			if ctx.Err() != nil {
				task.State = TaskTimedOut
				task.Error = "timed out after 120s"
				if out != "" {
					task.Result = fmt.Sprintf("[task %q timed out — partial result]\n%s", name, out)
				}
			} else {
				task.State = TaskFailed
				task.Error = err.Error()
				if stringsContains(err.Error(), "max steps") && out != "" {
					task.Result = fmt.Sprintf("[task %q hit max steps — partial result]\n%s", name, out)
					task.State = TaskDone
				}
			}
		} else {
			task.State = TaskDone
			task.Result = out
			tlog.Debug("task.bg", "done", "id", id, "output_size", len(out))
		}
		mgr.mu.Unlock()
		close(task.Done)
	}()

	return id
}

// Collect waits for a background task to complete and returns its result.
// Returns an error if the task ID is unknown.
func (mgr *BackgroundTaskManager) Collect(taskID string) (string, error) {
	mgr.mu.Lock()
	task, ok := mgr.tasks[taskID]
	mgr.mu.Unlock()

	if !ok {
		return "", fmt.Errorf("task_collect: unknown task %q", taskID)
	}

	// Wait for completion
	<-task.Done

	switch task.State {
	case TaskDone:
		return task.Result, nil
	case TaskTimedOut:
		if task.Result != "" {
			return task.Result, nil
		}
		return "", fmt.Errorf("task_collect: task %q timed out", taskID)
	case TaskFailed:
		if task.Result != "" {
			return task.Result, nil
		}
		return "", fmt.Errorf("task_collect: task %q failed: %s", taskID, task.Error)
	default:
		return "", fmt.Errorf("task_collect: task %q in unexpected state %v", taskID, task.State)
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
