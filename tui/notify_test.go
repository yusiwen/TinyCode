package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/internal/notify"
)

// notifyRecorder installs a recording sink and restores the built-in policy.
func notifyRecorder(t *testing.T) *bytes.Buffer {
	t.Helper()
	notify.Reset()
	t.Cleanup(notify.Reset)
	out := &bytes.Buffer{}
	notify.SetSink(out)
	notify.Configure(notify.Config{Channel: notify.ChannelTerminal, MinDuration: time.Minute})
	return out
}

// TestNotifyFinishedCmdAnnouncesALongRun: the command is the TUI's only path to a
// notification, and running it is what the event loop would do.
func TestNotifyFinishedCmdAnnouncesALongRun(t *testing.T) {
	out := notifyRecorder(t)

	cmd := notifyFinishedCmd(5*time.Minute, "run finished")
	if cmd == nil {
		t.Fatal("no command was returned for the terminal step")
	}
	if msg := cmd(); msg != nil {
		t.Errorf("the command returned %v, want no message: it only announces", msg)
	}

	if !strings.Contains(out.String(), "run finished") {
		t.Errorf("nothing was announced: %q", out.String())
	}
}

// TestNotifyFinishedCmdStaysQuietForAShortRun is the other side of the threshold:
// the TUI supplies the elapsed time and internal/notify decides.
func TestNotifyFinishedCmdStaysQuietForAShortRun(t *testing.T) {
	out := notifyRecorder(t)

	notifyFinishedCmd(2*time.Second, "run finished")()

	if out.Len() != 0 {
		t.Errorf("a two-second run announced itself despite a one-minute threshold: %q", out.String())
	}
}

// TestRunStartIsStamped is what makes the elapsed time real: a run that never
// records when it began would measure from the zero time and announce everything.
func TestRunStartIsStamped(t *testing.T) {
	m := layoutModel(30)

	before := time.Now()
	if _, _, ok := m.beginRun(); !ok {
		t.Fatal("beginRun refused")
	}
	if m.runStart.Before(before) {
		t.Errorf("runStart = %v, want a time at or after the call (%v)", m.runStart, before)
	}
}
