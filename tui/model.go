package tui

import (
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
)

// TuiModel is the main Bubble Tea model for TinyCode.
type TuiModel struct {
	agent    *agent.Agent
	config   *config.Config
	registry *agent.Registry

	// UI
	ready    bool
	width    int
	height   int
	messages []chatMessage
	vp       viewport.Model
	input    textarea.Model
	spinner  spinner.Model
	modeName string

	// Streaming state
	status       TuiStatus
	streamCh     chan tea.Msg
	curAssistant *chatMessage // current streaming assistant message

	// Input history
	lastInput string

	// Provider selection dialog
	selectingProvider bool
	providerCursor    int
}

// NewTUI creates and returns a new TUI model.
func NewTUI(ag *agent.Agent, cfg *config.Config, reg *agent.Registry) *TuiModel {
	t := textarea.New()
	t.Placeholder = "Type your request (Alt+Enter to send)..."
	t.CharLimit = 0
	t.SetWidth(80)
	t.ShowLineNumbers = false
	t.SetHeight(1)
	t.Focus()

	s := spinner.New()
	s.Style = spinnerStyle

	return &TuiModel{
		agent:    ag,
		config:   cfg,
		registry: reg,
		input:    t,
		spinner:  s,
		modeName: reg.CurrentName(),
		status:   StatusIdle,
		streamCh: make(chan tea.Msg, 200),
	}
}

// Init returns the initial commands.
func (m *TuiModel) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.spinner.Tick,
	)
}

// sendStreamMsg is a tea.Cmd that reads from the stream channel.
func (m *TuiModel) sendStreamMsg() tea.Msg {
	msg := <-m.streamCh
	return msg
}
