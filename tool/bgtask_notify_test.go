package tool

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/internal/notify"
)

// notifyRecorder installs a recording sink and restores the built-in policy when
// the test ends.
//
// The threshold is zero on purpose: these tests count announcements, and a
// non-zero minimum would hide a duplicate whose elapsed time is zero — which is
// exactly how the once-guard test passed for the wrong reason until a mutation
// showed it (the duplicate was swallowed by the threshold, not by the guard).
func notifyRecorder(t *testing.T) *bytes.Buffer {
	t.Helper()
	notify.Reset()
	t.Cleanup(notify.Reset)
	out := &bytes.Buffer{}
	notify.SetSink(out)
	notify.Configure(notify.Config{Channel: notify.ChannelTerminal, MinDuration: 0})
	return out
}

// runningTask puts a task in the manager in the running state, started ago.
func runningTask(mgr *BackgroundTaskManager, started time.Duration) *BgTask {
	task := &BgTask{
		ID:      "task_1",
		Agent:   "explore",
		Goal:    "map the call sites",
		State:   TaskRunning,
		Done:    make(chan struct{}),
		started: time.Now().Add(-started),
	}
	mgr.mu.Lock()
	mgr.tasks[task.ID] = task
	mgr.mu.Unlock()
	return task
}

// TestFinalizeAnnouncesOnce pins the once-guard: the worker and the watchdog can
// both settle a task, and the user must not be told twice.
func TestFinalizeAnnouncesOnce(t *testing.T) {
	out := notifyRecorder(t)
	mgr := NewBackgroundTaskManager()
	task := runningTask(mgr, 5*time.Minute)

	mgr.finalize(task, TaskDone, "", "the result")
	mgr.finalize(task, TaskFailed, "late and wrong", "")

	got := out.String()
	if n := strings.Count(got, "\x1b]9;"); n != 1 {
		t.Errorf("announcements = %d, want exactly 1 (%q)", n, got)
	}
	if !strings.Contains(got, "finished") {
		t.Errorf("the first state should be the one announced, got %q", got)
	}
	if strings.Contains(got, "failed") {
		t.Errorf("the late call changed the announcement: %q", got)
	}
	if !strings.Contains(got, "explore") || !strings.Contains(got, "map the call sites") {
		t.Errorf("the announcement should name the agent and the goal, got %q", got)
	}
}

// TestFinalizeAnnouncesOutsideTheLock: a Collect blocked on the manager's mutex
// must not be waiting on a notification, which writes to a terminal and can spawn
// a process. The sink proves it by taking the mutex — if the announcement ran
// under the lock, this probe blocks and the test fails on the bound instead of
// hanging.
func TestFinalizeAnnouncesOutsideTheLock(t *testing.T) {
	notify.Reset()
	t.Cleanup(notify.Reset)

	mgr := NewBackgroundTaskManager()
	task := runningTask(mgr, 5*time.Minute)

	probed := make(chan struct{})
	notify.SetSink(writerFunc(func(p []byte) (int, error) {
		go func() {
			// Status takes the manager's mutex. A held mutex means the
			// announcement is running inside finalize's critical section.
			mgr.Status(task.ID)
			close(probed)
		}()
		select {
		case <-probed:
		case <-time.After(5 * time.Second):
			t.Error("the manager's lock was held while the announcement ran: a Collect would wait on a notification")
		}
		return len(p), nil
	}))
	notify.Configure(notify.Config{Channel: notify.ChannelTerminal, MinDuration: 0})

	mgr.finalize(task, TaskDone, "", "out")
}

// TestFinalizeRespectsTheThreshold: a task that finished quickly is not worth
// interrupting anyone over, and the task path must not bypass that gate.
func TestFinalizeRespectsTheThreshold(t *testing.T) {
	out := notifyRecorder(t)
	notify.Configure(notify.Config{Channel: notify.ChannelTerminal, MinDuration: time.Minute})
	mgr := NewBackgroundTaskManager()
	task := runningTask(mgr, time.Second)

	mgr.finalize(task, TaskDone, "", "out")

	if out.Len() != 0 {
		t.Errorf("a one-second task announced itself despite a one-minute threshold: %q", out.String())
	}
}

// TestTaskFinishedMessageNamesTheOutcome keeps the three outcomes distinct: a
// timed-out task and a failed one are not the same news.
func TestTaskFinishedMessageNamesTheOutcome(t *testing.T) {
	cases := map[TaskState]string{
		TaskDone:     "finished",
		TaskFailed:   "failed",
		TaskTimedOut: "timed out",
	}
	for state, want := range cases {
		got := taskFinishedMessage("general", state, "do the thing")
		if !strings.Contains(got, want) {
			t.Errorf("state %v: %q does not contain %q", state, got, want)
		}
		if !strings.Contains(got, "general") || !strings.Contains(got, "do the thing") {
			t.Errorf("state %v: %q should name the agent and the goal", state, got)
		}
	}
	// An unknown state still says something rather than an empty word.
	if got := taskFinishedMessage("", TaskRunning, "goal"); !strings.Contains(got, "ended") {
		t.Errorf("an unsettled state = %q, want it to read as ended", got)
	}
}

// writerFunc adapts a function to io.Writer for the probe above.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
