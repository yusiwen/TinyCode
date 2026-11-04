package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/verify"
)

// testsReady gives the model a test setting whose command is deterministic.
func testsReady(t *testing.T, m *TuiModel, command string) {
	t.Helper()
	m.agent.Tests = &verify.Settings{Dir: t.TempDir(), Config: verify.Config{Command: command}}
}

// TestTestCommandRunsTheSuiteOffTheLoop: /test returns a command, and the result
// arrives as a message — the event loop never blocks on the suite.
func TestTestCommandRunsTheSuiteOffTheLoop(t *testing.T) {
	m := newTestTUI()
	testsReady(t, m, "echo '--- FAIL: TestBroken'; exit 1")

	_, cmd := m.handleCommand("/test")
	if cmd == nil {
		t.Fatal("/test did not return a command")
	}
	msg := cmd()
	done, ok := msg.(testDoneMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if done.err != nil || done.report.Passed {
		t.Fatalf("report = %+v err=%v", done.report, done.err)
	}
}

// TestTestDoneReportsTheOutcome: the message becomes one transcript line.
func TestTestDoneReportsTheOutcome(t *testing.T) {
	m := newTestTUI()
	m.Update(testDoneMsg{report: verify.Report{Command: "go test", Passed: true, Duration: time.Second}})

	if !strings.Contains(lastMessage(m), "tests passed") {
		t.Fatalf("reply = %q", lastMessage(m))
	}

	m.Update(testDoneMsg{err: errors.New("no test command found")})
	if !strings.Contains(lastMessage(m), "no test command found") {
		t.Fatalf("error reply = %q", lastMessage(m))
	}
}

// TestTestCommandRefusedDuringARun: the run is writing the tree the tests read.
func TestTestCommandRefusedDuringARun(t *testing.T) {
	m := newTestTUI()
	testsReady(t, m, "echo ok")
	m.runMu.Lock()
	m.runActive = true
	m.runMu.Unlock()
	t.Cleanup(func() {
		m.runMu.Lock()
		m.runActive = false
		m.runMu.Unlock()
	})

	_, cmd := m.handleCommand("/test")

	if cmd != nil {
		t.Fatal("a test command was started during a run")
	}
	if !strings.Contains(m.statusMsg, "a run is in progress") {
		t.Fatalf("status = %q", m.statusMsg)
	}
}
