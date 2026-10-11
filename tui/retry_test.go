package tui

import (
	"errors"
	"strings"
	"testing"
)

// TestRetryMsgShowsInTheStatusBar: a backoff with nothing on screen is
// indistinguishable from a hang, which is the whole reason the event exists.
func TestRetryMsgShowsInTheStatusBar(t *testing.T) {
	m := layoutModel(30)

	m.Update(RetryMsg{Attempt: 2, MaxAttempts: 3, Err: errors.New("status 503: busy")})

	bar := m.renderStatusBar()
	if !strings.Contains(bar, "retrying (2/3)") {
		t.Errorf("bar = %q, want the attempt and its cap", bar)
	}
	if !strings.Contains(bar, "status 503") {
		t.Errorf("bar = %q, want the reason kept", bar)
	}
}

// TestRetryMsgFromASupersededRunIsIgnored keeps a cancelled run's status message
// from overwriting the current one.
func TestRetryMsgFromASupersededRunIsIgnored(t *testing.T) {
	m := layoutModel(30)
	m.runActive = true
	m.runID = 7

	m.Update(RetryMsg{RunID: 6, Attempt: 2, MaxAttempts: 3, Err: errors.New("status 503: busy")})

	if strings.Contains(m.renderStatusBar(), "retrying") {
		t.Errorf("bar = %q, want a superseded run's retry ignored", m.renderStatusBar())
	}
}
