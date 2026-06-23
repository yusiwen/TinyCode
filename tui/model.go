package tui

import (
	"encoding/json"
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
	sessionTitle      string // auto-generated conversation title
	sessionToolCalls  int

	// LSP diagnostics tracking
	diagTotal   int    // total errors across all files
	diagFile    string // most recent file with errors (for display)

	// Todo store
	todoStore *tool.TodoStore

	// Todo rendering tracking (virtual message in CellGrid)
	todoRowCount int  // rendered row count for todo section (0 = no items)
	todoDirty    bool // true = todo section needs re-render

	// renderAckCh is set when a todo tool call needs render confirmation.
	// The agent goroutine blocks on this channel; View() signals it after
	// the TODO is injected into the CellGrid.
	renderAckCh chan struct{}

	// Command palette
	cmdPalette      bool   // floating command palette active
	cmdPaletteInput string // current filter text (without the /)
	cmdPaletteSel   int    // selected index

	// Dialog overlay
	dialogMode      bool       // test dialog active
	dialogItems     []string   // dialog option labels
	dialogSel       int        // selected option index
	dialogResult    string     // selected result or empty
	dialogMsg       string     // dialog heading message
	dialogOnDone    func(string)  // callback invoked with selected value
	dialogOnCancel  func()        // callback invoked when dialog is cancelled
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
	// Clear textarea background styles so it inherits terminal default (matches message area)
	t.FocusedStyle.Base = t.FocusedStyle.Base.UnsetBackground()
	t.FocusedStyle.CursorLine = t.FocusedStyle.CursorLine.UnsetBackground()
	t.BlurredStyle.Base = t.BlurredStyle.Base.UnsetBackground()
	t.BlurredStyle.CursorLine = t.BlurredStyle.CursorLine.UnsetBackground()
	t.Focus()
	// Create spinner
	s := spinner.New()
	s.Style = spinnerStyle
	s.Spinner = spinner.Line

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

	// Recover todo state from session history (find most recent todo result)
	if todoStore != nil && len(m.messages) > 0 {
		for i := len(m.messages) - 1; i >= 0; i-- {
			msg := m.messages[i]
			if msg.Role == "assistant" && strings.Contains(msg.Content, "\"todos\"") {
				var result tool.TodoResult
				if err := json.Unmarshal([]byte(msg.Content), &result); err == nil && len(result.Todos) > 0 {
					todoStore.Write(result.Todos, false)
					break
				}
			}
		}
	}

	// Apply theme from config
	if cfg.Theme != "" {
		if t := LookupTheme(cfg.Theme); t != nil {
			ApplyTheme(*t)
		}
	}

	// Startup status message — welcome banner for new sessions
	if ag != nil && len(ag.Tools) > 0 {
		toolCount := len(ag.Tools)
		skillCount := len(skill.Discover("."))
		resumed := len(resume) > 0
		
		if resumed {
			msg := fmt.Sprintf("_Resumed session: %s_\n\nTinyCode ready — %d tools, %d skills loaded", resume[0], toolCount, skillCount)
			m.messages = append(m.messages, chatMessage{Role: "system", Content: msg})
		} else {
			msg := fmt.Sprintf("## Welcome to TinyCode\n\n**%d tools · %d skills · %d agents**\n\nGet started:\n- Type a message and press Enter to chat\n- `/help` — show all commands\n- `/model` — switch provider/model\n- `/plan` / `/build` — switch agent mode\n- `Ctrl+J` — new line in input\n\n_Config: ~/.tinycode/config.json_\n_Source: github.com/yusiwen/TinyCode_", toolCount, skillCount, 6)
			m.messages = append(m.messages, chatMessage{Role: "system", Content: msg})
		}
	}

	return m
}

// checkPermissionDialog auto-shows the permission dialog when a sandbox
// path is pending approval. Returns true if dialog was shown.
func (m *TuiModel) checkPermissionDialog() bool {
	if !tool.HasPendingPermission() || m.dialogMode {
		return false
	}
	path := tool.PendingPermissionPath()
	label := tool.PendingPermissionAgentLabel()
	if path == "" {
		return false
	}
	displayPath := path
	if len(displayPath) > 60 {
		displayPath = "..." + displayPath[len(displayPath)-57:]
	}
	title := "🔒 Write to " + displayPath + "?"
	if label != "" {
		title = "🔒 [" + label + "] Write to " + displayPath + "?"
	}
	m.showDialogWithCancel(title, []string{
		"Allow once",
		"Always allow",
		"Deny",
	}, func(sel string) {
		var allowed bool
		var mode string
		switch sel {
		case "Allow once":
			allowed = true
			mode = "once"
		case "Always allow":
			allowed = true
			mode = "always"
		default:
			allowed = false
			mode = "denied"
		}
		tool.ResolvePermission(tool.PendingPermissionPath(), allowed, mode)
	}, func() {
		tool.ResolvePermission(tool.PendingPermissionPath(), false, "cancelled")
	})
	return true
}

// showDialog activates the dialog overlay with the given items.
func (m *TuiModel) showDialog(msg string, items []string, onDone func(string)) {
	m.showDialogWithCancel(msg, items, onDone, nil)
}

// showDialogWithCancel is like showDialog but also accepts a cancel callback.
func (m *TuiModel) showDialogWithCancel(msg string, items []string, onDone func(string), onCancel func()) {
	m.dialogMsg = msg
	m.dialogItems = items
	m.dialogSel = 0
	m.dialogResult = ""
	m.dialogOnDone = onDone
	m.dialogOnCancel = onCancel
	m.dialogMode = true
}

// handleDialogKey processes keyboard input while dialog is active.
func (m *TuiModel) handleDialogKey(msg tea.KeyMsg) tea.Model {
	switch msg.Type {
	case tea.KeyUp:
		if m.dialogSel > 0 {
			m.dialogSel--
		}
		return m
	case tea.KeyDown:
		if m.dialogSel < len(m.dialogItems)-1 {
			m.dialogSel++
		}
		return m
	case tea.KeyEnter:
		sel := m.dialogSel
		if sel >= 0 && sel < len(m.dialogItems) {
			m.dialogResult = m.dialogItems[sel]
			if m.dialogOnDone != nil {
				m.dialogOnDone(m.dialogResult)
			}
		}
		m.dialogMode = false
		m.dialogOnDone = nil
		return m
	case tea.KeyEscape, tea.KeyCtrlC:
		m.dialogResult = ""
		cb := m.dialogOnCancel
		m.dialogMode = false
		m.dialogOnDone = nil
		m.dialogOnCancel = nil
		if cb != nil {
			cb()
		}
		return m
	default:
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
			ch := msg.Runes[0]
			if ch >= '1' && ch <= '9' {
				idx := int(ch - '1')
				if idx >= 0 && idx < len(m.dialogItems) {
					m.dialogResult = m.dialogItems[idx]
					if m.dialogOnDone != nil {
						m.dialogOnDone(m.dialogResult)
					}
					m.dialogMode = false
					m.dialogOnDone = nil
				}
			}
		}
		return m
	}
}

// Init returns the initial commands.
func (m *TuiModel) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		m.spinner.Tick,
	)
}

// clearCharSelection clears the character-level selection highlight from
// both the model state and the CellGrid cells.
func (m *TuiModel) clearCharSelection() {
	if m.charSelStart.Offset >= 0 {
		// Force full grid rebuild to erase SelectionStyle from all cells
		if m.grid != nil {
			m.grid = NewCellGrid(m.grid.width, 10)
		}
		m.MarkAllDirty()
	}
	m.charSelStart = selPos{Offset: -1}
	m.charSelEnd = selPos{Offset: -1}
	m.charSelStartLine = 0
	m.charSelStartCol = 0
	m.charSelEndLine = 0
	m.charSelEndCol = 0
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
