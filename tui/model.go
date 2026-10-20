package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/goal"
	"github.com/yusiwen/tinycode/lsp"
	"github.com/yusiwen/tinycode/session"
	"github.com/yusiwen/tinycode/skill"
	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/types"
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
	MsgIdx        int
	SourceField   string // "content" / "reasoning" / "label" / "user" / "system" / "button"
	Text          string // plain text for extraction
	CharStart     int
	CharEnd       int
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

	// Run lifecycle: only one agent run may be active at a time. runID is a
	// generation counter stamped on every stream message so output from a
	// superseded run can be ignored. All fields are guarded by runMu because
	// the agent goroutine and Update may both touch them.
	runMu     sync.Mutex
	runID     uint64
	runActive bool
	// runStart is when the active run began. It is what the finish notification
	// measures: a run has to outlive the threshold before it is worth announcing,
	// because interrupting someone for a two-second answer is noise.
	runStart       time.Time
	runCancel      context.CancelFunc
	runInterrupted bool

	// Manual compression lifecycle. /compress runs its summarizer off the
	// event loop, so it needs its own cancelable lifecycle; these fields share
	// runMu with the run state because the two are mutually exclusive — both
	// read or replace Agent.History, so neither may overlap the other.
	compressActive      bool
	compressCancel      context.CancelFunc
	compressInterrupted bool

	// Input history
	lastInput string

	// Provider selection dialog
	selectingProvider bool
	providerCursor    int

	// Mouse selection (message-level — deprecated, to be replaced)
	selecting   bool
	mouseDrag   bool
	selectStart int
	selectEnd   int

	// Character-level selection (new)
	charSelStart     selPos
	charSelEnd       selPos
	charSelStartLine int
	charSelStartCol  int
	charSelEndLine   int
	charSelEndCol    int
	lineSrcs         []lineSrc

	// Buttons (rebuilt each View)
	activeButtons []Button

	// CellGrid (reused across renders)
	grid *CellGrid

	// Incremental render tracking
	msgRowCount []int  // rendered row count per message (0 = not yet rendered)
	msgDirty    []bool // true = needs re-render

	// Status bar message (transient, replaces system messages)
	statusMsg string

	// Input history (up/down arrows)
	inputHistory []string
	historyPos   int    // -1 = current draft, 0+ = history index
	historyDraft string // saved current input when browsing history

	// Quit confirmation
	quitConfirm bool

	// Session persistence
	SessionDir    string
	currentBranch string // current branch ID (same as main session ID for main)
	sessionStore  *session.Store

	// Scroll tracking
	streamDoneNotified bool // true after first GotoBottom on stream completion

	// Session stats
	sessionStart  time.Time
	sessionTokens int
	// callTokens is the estimate charged to the LLM call currently streaming,
	// in the same units as sessionTokens. A reported usage replaces exactly that
	// much of the session counter and then clears it; it is cleared as well when
	// a new assistant message starts, so a call that reported nothing keeps its
	// estimate instead of donating it to the next call's correction.
	callTokens       int
	sessionTitle     string // auto-generated conversation title
	sessionToolCalls int
	// sessionCached is the part of the session's prompt tokens a provider served
	// from its cache, summed from reported usage (roadmap A4). It is a subset of
	// the prompt tokens counted in sessionTokens, not a separate counter, and it
	// stays zero until a provider reports a hit, so a route that does not cache
	// leaves the bar unchanged.
	sessionCached int
	// sessionCost is the money this session is known to have cost, per unit, and
	// unpricedCalls counts the calls whose cost is not known. The bar shows both,
	// because a total without its unknowns is a different claim from a total
	// alone.
	sessionCost   types.CostTotals
	unpricedCalls int

	// LSP diagnostics tracking
	diagTotal   int      // total errors across all files
	diagFiles   int      // number of files with errors
	diagFile    string   // most recent file with errors (for display)
	diagDetails []string // one line per affected file
	// diagSource reads the current LSP diagnostics snapshot. It is a field so
	// tests can inject a stub; it defaults to lsp.DiagnosticsSnapshot.
	diagSource func() lsp.DiagnosticsInfo

	// Todo store
	todoStore *tool.TodoStore

	// goals is the session's completion objective and its process-local
	// permission to continue. It is the only writer of that state; the
	// session file is written by persistSession, on this goroutine.
	goals *goal.Service
	// currentRunIsGoal marks the run in flight as one the continuation driver
	// started, so a cancel or a failure pauses the goal instead of silently
	// leaving it armed. It is written and read on this goroutine only.
	currentRunIsGoal bool
	// goalToolFreeRounds counts consecutive goal rounds that made no tool
	// calls, and runToolCallsStart records where the session counter stood when
	// the run began. Together they are the no-progress guard.
	goalToolFreeRounds int
	runToolCallsStart  int
	// runGoalPhase is the goal's phase when the run began, so a run that
	// completed or blocked the goal can be announced once rather than on every
	// later run of the same session.
	runGoalPhase goal.Phase
	// goalGrace and goalNoProgress are the continuation policy this session
	// runs with, read from the config once so a mid-session file edit cannot
	// change the rules under a running round.
	goalGrace      time.Duration
	goalNoProgress int

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
	dialogMode     bool         // test dialog active
	dialogItems    []string     // dialog option labels
	dialogSel      int          // selected option index
	dialogResult   string       // selected result or empty
	dialogMsg      string       // dialog heading message
	dialogOnDone   func(string) // callback invoked with selected value
	dialogOnCancel func()       // callback invoked when dialog is cancelled
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
		{"/undo", "Restore the last tool call's files"},
		{"/goal", "View, create, pause or resume the goal"},
		{"/sandbox", "Sandbox status"},
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

// historyFromSession turns a persisted transcript back into the multi-turn
// history the agent loop sends as context.
//
// Only user and assistant turns are restored. A "system" entry in the session
// is command output the interface rendered ("Unknown command", "Switched
// theme", …), not dialogue, and feeding it to the model would invent a
// conversation that never happened. Reasoning is dropped on purpose: the
// provider resends reasoning_content verbatim, and a thinking block from an
// earlier turn is not input the model should read back.
func historyFromSession(msgs []types.Message) []types.Message {
	history := make([]types.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role != types.RoleUser && m.Role != types.RoleAssistant {
			continue
		}
		if strings.TrimSpace(m.Content) == "" {
			continue
		}
		history = append(history, types.Message{Role: m.Role, Content: m.Content})
	}
	return history
}

// adoptSession makes branch the current session: transcript, model context,
// task list, goal and per-session counters all change together.
//
// It is one function on purpose. The defect it replaces (issue #270) was a
// switch that replaced the screen and left the rest behind — a process-global
// todo list was snapshotted into the new session and injected into its model
// context, the model kept the old conversation, and a fork kept the parent's
// armed goal while the branch's own record stayed untouched.
func (m *TuiModel) adoptSession(branch *session.Session) {
	m.messages = nil
	for _, sm := range branch.Messages {
		cm := chatMessage{
			Role:             sm.Role,
			Content:          sm.Content,
			ReasoningContent: sm.ReasoningContent,
		}
		if sm.Role == types.RoleAssistant && sm.Content != "" {
			cm.Blocks = parseMarkdown(sm.Content)
		}
		m.messages = append(m.messages, cm)
	}
	// The model's copy is rebuilt with the same rule a resume uses: user and
	// assistant turns only, never command output and never prior reasoning.
	m.agent.History = historyFromSession(branch.Messages)
	if m.todoStore != nil {
		// Replace, never merge: an empty list has to clear the previous
		// session's items, which is exactly what a merge would keep.
		if err := m.todoStore.Write(branch.Todos, false); err != nil {
			m.appendSystem(fmt.Sprintf("The task list saved with session %q is not usable: %v", branch.ID, err))
		}
	}
	if m.goals != nil {
		// A goal survives the file; its permission to continue does not.
		if err := m.goals.Restore(branch.Goal); err != nil {
			m.appendSystem(fmt.Sprintf("The goal saved with session %q is not usable: %v", branch.ID, err))
		}
	}
	m.currentBranch = branch.ID
	m.resetSessionStats()
}

// NewTUI creates and returns a new TUI model.
func NewTUI(ag *agent.Agent, cfg *config.Config, reg *agent.Registry, provReg *agent.ProviderRegistry, todoStore *tool.TodoStore, goals *goal.Service, resume ...string) *TuiModel {
	if goals == nil {
		// A caller that does not care about the goal still gets one, so the
		// command and the driver never have to test for its absence.
		goals = goal.NewService()
	}
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
	s.Spinner = spinner.Dot

	m := &TuiModel{
		agent:          ag,
		config:         cfg,
		registry:       reg,
		provReg:        provReg,
		input:          t,
		spinner:        s,
		todoStore:      todoStore,
		goals:          goals,
		goalGrace:      time.Duration(cfg.GoalSettings().IdleGraceSeconds) * time.Second,
		goalNoProgress: cfg.GoalSettings().NoProgressRounds,
		modeName:       reg.CurrentName(),
		status:         StatusIdle,
		streamCh:       make(chan tea.Msg, 200),
		selectStart:    -1,
		selectEnd:      -1,
		charSelStart:   selPos{Offset: -1},
		charSelEnd:     selPos{Offset: -1},
		sessionStart:   time.Now(),
		SessionDir:     cfg.SessionDir,
		currentBranch:  "", // no session yet
		sessionStore:   session.NewStore(cfg.SessionDir),
		diagSource:     lsp.DiagnosticsSnapshot,
	}

	// Load session if resume ID provided
	resumedOK := false
	if len(resume) > 0 && resume[0] != "" {
		sess, err := session.Load(resume[0], cfg.SessionDir)
		if err == nil {
			resumedOK = true
			// Everything the session owns — transcript, model context, todos,
			// goal, counters — changes in one place (issue #270).
			m.adoptSession(sess)
			// Restore provider/model from session
			if sess.ModelName != "" && m.provReg != nil {
				m.provReg.SwitchToName(sess.ModelName)
			}
			// Restore allowed paths from session (Allow session)
			for _, p := range sess.AllowedPaths {
				tool.DefaultSandbox.AllowAlways(p)
			}
		} else {
			m.messages = append(m.messages, chatMessage{
				Role:    "system",
				Content: fmt.Sprintf("Failed to resume session %q: %v", resume[0], err),
			})
		}
	}

	// Restore input history from loaded user messages
	for _, cm := range m.messages {
		if cm.Role == "user" && cm.Content != "" {
			if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != cm.Content {
				m.inputHistory = append(m.inputHistory, cm.Content)
			}
		}
	}
	// Reset history position — zero value (0) would wrongly skip the
	// "first press" branch on Up. -1 means "not browsing" as expected.
	m.historyPos = -1

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

		if resumedOK {
			msg := fmt.Sprintf("_Resumed session: %s_\n\nTinyCode ready — %d tools, %d skills loaded", resume[0], toolCount, skillCount)
			m.messages = append(m.messages, chatMessage{Role: "system", Content: msg})
		} else {
			agentCount := 0
			if reg != nil {
				agentCount = len(reg.List())
			}
			m.messages = append(m.messages, newWelcomeMessage(welcomeInfo{
				Tools:  toolCount,
				Skills: skillCount,
				Agents: agentCount,
			}))
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
	// Capture the request identity now: the queue head can change while the
	// user is deciding, and the answer must apply to the request that was shown.
	reqID := tool.PendingPermissionID()
	reqPath := path
	m.showDialogWithCancel(title, []string{
		allowOnceLabel,
		allowSessionLabel,
		alwaysAllowLabel(),
		"Deny",
	}, func(sel string) {
		var allowed bool
		var mode string
		switch {
		case sel == allowOnceLabel:
			allowed = true
			mode = "once"
		case sel == allowSessionLabel:
			allowed = true
			mode = "session"
		case strings.HasPrefix(sel, alwaysAllowPrefix):
			allowed = true
			mode = "always"
		default:
			allowed = false
			mode = "denied"
		}
		if allowed && mode == "session" && reqPath != "" {
			// Persist to session file on Allow session
			if m.SessionDir != "" && m.currentBranch != "" {
				// Read existing session, append path, write back
				if existing, err := session.Load(m.currentBranch, m.SessionDir); err == nil {
					existing.AllowedPaths = appendUniquePath(existing.AllowedPaths, reqPath)
					existing.Flush()
				} else {
					// session.New sets the unexported dir field, so the file
					// lands in SessionDir (a bare struct literal would write to
					// the process working directory).
					sess := session.New(m.currentBranch, m.SessionDir)
					sess.AllowedPaths = []string{reqPath}
					sess.Containment = tool.ContainmentInfo().String()
					sess.Flush()
				}
			}
		}
		if allowed && mode == "always" && reqPath != "" {
			// Persist to the user's global config on "Always allow". Only the
			// sandbox grant keys are touched, so project-local (untrusted)
			// values and today's defaults are never written to the user file.
			//
			// The project goes in with the grant, so a later reader can tell
			// where a path was allowed from; the label the person chose already
			// named the file this writes.
			if err := config.AddAllowedPathGrant(reqPath, tool.DefaultSandbox.ProjectRoot); err != nil {
				tlog.Warn("tui.permission", "save_config_error", "err", err)
			}
		}
		tool.ResolvePermissionByID(reqID, allowed, mode)
	}, func() {
		tool.ResolvePermissionByID(reqID, false, "cancelled")
	})
	return true
}

// appendUniquePath appends path unless it is already present.
func appendUniquePath(paths []string, path string) []string {
	for _, p := range paths {
		if p == path {
			return paths
		}
	}
	return append(paths, path)
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

// resetSessionStats clears the per-session token and tool-call counters when
// the transcript is replaced by a different conversation (fork or branch
// switch), so the status bar never shows stats from another conversation.
func (m *TuiModel) resetSessionStats() {
	m.sessionTokens = 0
	m.callTokens = 0
	m.sessionCached = 0
	m.sessionToolCalls = 0
	m.sessionCost = nil
	m.unpricedCalls = 0
}

// ensureMsgTracking ensures msgDirty and msgRowCount arrays match m.messages length.
func (m *TuiModel) ensureMsgTracking() {
	for len(m.msgDirty) < len(m.messages) {
		m.msgDirty = append(m.msgDirty, true)
		m.msgRowCount = append(m.msgRowCount, 0)
	}
}
