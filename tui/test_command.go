package tui

import (
	"context"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yusiwen/tinycode/verify"
)

// testDoneMsg carries the outcome of a manual /test. It exists so the suite runs
// off the event loop: a test command can take a minute, and freezing the TUI for
// it would be the one thing a feedback loop must not do.
type testDoneMsg struct {
	report verify.Report
	err    error
}

// testCmd runs the project's suite in the background and reports back.
func (m *TuiModel) testCmd() tea.Cmd {
	settings := *m.agent.Tests
	return func() tea.Msg {
		command, err := verify.Discover(settings.Dir, settings.Config)
		if err != nil {
			return testDoneMsg{err: err}
		}
		report, err := verify.Run(context.Background(), settings.Dir, command, verify.Options{
			Timeout:   time.Duration(settings.Config.TimeoutMS) * time.Millisecond,
			MaxOutput: settings.Config.MaxOutputBytes,
		})
		return testDoneMsg{report: report, err: err}
	}
}
