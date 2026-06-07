package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
)

// Button represents a clickable region in the message area.
type Button struct {
	MsgIdx int
	Line   int
	Col    int
	Width  int
	Label  string
	Action func()
}

// lineSrc records the source of each rendered line in msgLines.
type lineSrc struct {
	MsgIdx      int
	SourceField string // "content" / "reasoning" / "label" / "user" / "system" / "button"
	Text        string // plain text for extraction
	CharStart   int
	CharEnd     int
}

// selPos represents one endpoint of a character-level selection.
type selPos struct {
	MsgIdx int
	Offset int // char offset in Content or ReasoningContent (-1 = none)
}

// TuiModel is the main Bubble Tea model for the chat interface.
type TuiModel struct {
	agent    *agent.Agent
	config   *config.Config
	registry *agent.Registry
	provReg  *agent.ProviderRegistry

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

	// Mouse selection (message-level — deprecated, to be replaced)
	selecting      bool
	mouseDrag      bool
	selectStart    int
	selectEnd      int

	// Character-level selection (new)
	charSelStart selPos
	charSelEnd   selPos
	lineSrcs     []lineSrc

	// Buttons (rebuilt each View)
	activeButtons []Button

	// Quit confirmation
	quitConfirm bool

	// Scroll tracking
	streamDoneNotified bool // true after first GotoBottom on stream completion

	// Session stats
	sessionStart      time.Time
	sessionTokens     int
	sessionToolCalls  int
}

// NewTUI creates and returns a new TUI model.
func NewTUI(ag *agent.Agent, cfg *config.Config, reg *agent.Registry, provReg *agent.ProviderRegistry) *TuiModel {
	t := textarea.New()
	t.Placeholder = "Type your request (Ctrl+J for newline)..."
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
		provReg:  provReg,
		input:    t,
		spinner:  s,
		modeName: reg.CurrentName(),
		status:   StatusIdle,
		streamCh: make(chan tea.Msg, 200),
		selectStart: -1,
		selectEnd:   -1,
		sessionStart: time.Now(),
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
