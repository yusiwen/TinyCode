package tui

import (
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/session"
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
	ContentOffset int // bytes of prefix ("> ", "→ ", "    ") to skip for msg.Content offset
}

// selPos represents one endpoint of a character-level selection.
type selPos struct {
	MsgIdx int
	Field  string // "content" / "reasoning" - which field Offset refers to
	Offset int    // char offset in the selected content field (-1 = none)
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
	charSelStartLine int
	charSelStartCol  int
	charSelEndLine   int
	charSelEndCol    int
	lineSrcs     []lineSrc

	// Buttons (rebuilt each View)
	activeButtons []Button

	// Input history (up/down arrows)
	inputHistory  []string
	historyPos    int    // -1 = current draft, 0+ = history index
	historyDraft  string // saved current input when browsing history

	// Quit confirmation
	quitConfirm bool

	// Session persistence
	SessionDir string

	// Scroll tracking
	streamDoneNotified bool // true after first GotoBottom on stream completion

	// Session stats
	sessionStart      time.Time
	sessionTokens     int
	sessionToolCalls  int
}

// NewTUI creates and returns a new TUI model.
func NewTUI(ag *agent.Agent, cfg *config.Config, reg *agent.Registry, provReg *agent.ProviderRegistry, resume ...string) *TuiModel {
	t := textarea.New()
	t.Placeholder = "Type your request (Ctrl+J for newline)..."
	t.CharLimit = 0
	t.SetWidth(80)
	t.ShowLineNumbers = false
	t.SetHeight(1)
	t.Focus()

	s := spinner.New()
	s.Style = spinnerStyle

	m := &TuiModel{
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
		charSelStart: selPos{Offset: -1},
		charSelEnd:   selPos{Offset: -1},
		sessionStart: time.Now(),
		SessionDir:   cfg.SessionDir,
	}

	// Load session if resume ID provided
	if len(resume) > 0 && resume[0] != "" {
		sess, err := session.Load(resume[0], cfg.SessionDir)
		if err == nil {
			for _, sm := range sess.Messages {
				cm := chatMessage{
					Role:             sm.Role,
					Content:          sm.Content,
					ReasoningContent: sm.ReasoningContent,
				}
				// Parse markdown for assistant messages with content
				if sm.Role == "assistant" && sm.Content != "" {
					cm.Blocks = parseMarkdown(sm.Content)
				}
				m.messages = append(m.messages, cm)
			}
		}
	}

	return m
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
