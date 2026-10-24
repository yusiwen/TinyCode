package tui

import (
	"errors"
	"strings"
	"testing"
)

// TestFailoverMsgShowsInTheStatusBar: a route change moves the call to another
// account and another model, so it cannot be silent (roadmap A5, S3).
func TestFailoverMsgShowsInTheStatusBar(t *testing.T) {
	m := layoutModel(80)

	m.Update(FailoverMsg{From: "deepseek", To: "openrouter", Err: errors.New("status 429: slow down")})

	bar := m.renderStatusBar()
	if !strings.Contains(bar, "routing deepseek → openrouter") {
		t.Errorf("bar = %q, want the route change", bar)
	}
	if !strings.Contains(bar, "status 429") {
		t.Errorf("bar = %q, want the reason kept", bar)
	}
}

// TestFailoverMsgFromASupersededRunIsIgnored mirrors the retry rule: a cancelled
// run must not paint over the current one's status.
func TestFailoverMsgFromASupersededRunIsIgnored(t *testing.T) {
	m := layoutModel(80)
	m.runActive = true
	m.runID = 7

	m.Update(FailoverMsg{RunID: 6, From: "deepseek", To: "openrouter", Err: errors.New("status 429")})

	if strings.Contains(m.renderStatusBar(), "routing") {
		t.Errorf("bar = %q, want a superseded run's failover ignored", m.renderStatusBar())
	}
}
