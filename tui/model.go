package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/session"
	"github.com/yusiwen/tinycode/skill"
	"github.com/yusiwen/tinycode/tool"
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

	// CellGrid (reused across renders)
	grid *CellGrid

	// Incremental render tracking
	msgRowCount []int      // rendered row count per message (0 = not yet rendered)
	msgDirty    []bool     // true = needs re-render

	// Status bar message (transient, replaces system messages)
	statusMsg string

	// Input history (up/down arrows)
	inputHistory  []string
	historyPos    int    // -1 = current draft, 0+ = history index
	historyDraft  string // saved current input when browsing history

	// Quit confirmation
	quitConfirm bool

	// Session persistence
	SessionDir      string
	currentBranch   string // current branch ID (same as main session ID for main)
	sessionStore    *session.Store

	// Scroll tracking
	streamDoneNotified bool // true after first GotoBottom on stream completion

	// Session stats
	sessionStart      time.Time
	sessionTokens     int
	sessionToolCalls  int

	// LSP diagnostics tracking
	diagTotal   int    // total errors across all files
	diagFile    string // most recent file with errors (for display)

	// Todo store
	todoStore *tool.TodoStore

	// Command palette
	cmdPalette      bool   // floating command palette active
	cmdPaletteInput string // current filter text (without the /)
	cmdPaletteSel   int    // selected index
}

// cmdEntry describes one command in the floating palette.
type cmdEntry struct {
	Name string
	Desc string
}

// commandList returns all available commands for the palette.
func commandList() []cmdEntry {
	return []cmdEntry{
		{"/help", "Show help"},
		{"/plan", "Plan mode"},
		{"/build", "Build mode"},
		{"/verbose", "Toggle verbose output"},
		{"/thinking", "Toggle thinking display"},
		{"/theme", "Switch theme"},
		{"/skill", "Load a skill"},
		{"/diagnostics", "LSP diagnostics"},
		{"/model", "Switch model"},
		{"/fork", "Create session branch"},
		{"/session", "List/switch branch"},
		{"/exit", "Exit TinyCode"},
	}
}

// filteredCmds returns commands matching the current filter.
func (m *TuiModel) filteredCmds() []cmdEntry {
	all := commandList()
	if !m.cmdPalette || m.cmdPaletteInput == "" {
		return all
	}
	lower := strings.ToLower(m.cmdPaletteInput)
	var filtered []cmdEntry
	for _, c := range all {
		if strings.Contains(strings.ToLower(c.Name), lower) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

// NewTUI creates and returns a new TUI model.
func NewTUI(ag *agent.Agent, cfg *config.Config, reg *agent.Registry, provReg *agent.ProviderRegistry, todoStore *tool.TodoStore, resume ...string) *TuiModel {
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
		agent:     ag,
		config:    cfg,
		registry:  reg,
		provReg:   provReg,
		input:     t,
		spinner:   s,
		todoStore: todoStore,
		modeName: reg.CurrentName(),
		status:   StatusIdle,
		streamCh: make(chan tea.Msg, 200),
		selectStart: -1,
		selectEnd:   -1,
		charSelStart: selPos{Offset: -1},
		charSelEnd:   selPos{Offset: -1},
		sessionStart: time.Now(),
		SessionDir:      cfg.SessionDir,
		currentBranch:   "", // no session yet
		sessionStore:    session.NewStore(cfg.SessionDir),
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
				if sm.Role == "assistant" && sm.Content != "" {
					cm.Blocks = parseMarkdown(sm.Content)
				}
				m.messages = append(m.messages, cm)
			}
			// Restore provider/model from session
			if sess.ModelName != "" && m.provReg != nil {
				m.provReg.SwitchToName(sess.ModelName)
			}
		}
	}

	// Apply theme from config
	if cfg.Theme != "" {
		if t := LookupTheme(cfg.Theme); t != nil {
			ApplyTheme(*t)
		}
	}

	// Startup status message
	if ag != nil && len(ag.Tools) > 0 {
		toolCount := len(ag.Tools)
		skillCount := len(skill.Discover("."))
		msg := fmt.Sprintf("TinyCode ready — %d tools, %d skills loaded", toolCount, skillCount)
		m.messages = append(m.messages, chatMessage{Role: "system", Content: msg})
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

// ShowStatus sets a transient status bar message.
func (m *TuiModel) ShowStatus(msg string) {
	m.statusMsg = msg
}

// MarkMsgDirty marks a message and all downstream messages for re-render.
func (m *TuiModel) MarkMsgDirty(idx int) {
	for i := idx; i < len(m.msgDirty); i++ {
		m.msgDirty[i] = true
	}
}

// MarkAllDirty marks all messages for re-render (theme switch, branch switch).
func (m *TuiModel) MarkAllDirty() {
	for i := range m.msgDirty {
		m.msgDirty[i] = true
	}
}

// ensureMsgTracking ensures msgDirty and msgRowCount arrays match m.messages length.
func (m *TuiModel) ensureMsgTracking() {
	for len(m.msgDirty) < len(m.messages) {
		m.msgDirty = append(m.msgDirty, true)
		m.msgRowCount = append(m.msgRowCount, 0)
	}
}
