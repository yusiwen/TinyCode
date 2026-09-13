package tui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/lsp"
	"github.com/yusiwen/tinycode/session"
	"github.com/yusiwen/tinycode/skill"
	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/types"
	"os"
)

func (m *TuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		if !m.ready {
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelUp {
			m.vp.LineUp(3)
			return m, nil
		}
		if msg.Button == tea.MouseButtonWheelDown {
			m.vp.LineDown(3)
			return m, nil
		}
		if msg.Button == tea.MouseButtonLeft {
			contentLine := msg.Y + m.vp.YOffset
			contentCol := msg.X

			// Check button clicks (Press only)
			if msg.Action == tea.MouseActionPress {
				for _, btn := range m.activeButtons {
					if contentLine == btn.Line && contentCol >= btn.Col && contentCol <= btn.Col+btn.Width {
						btn.Action()
						return m, nil
					}
				}
			}

			// Character-level selection (new)
			switch msg.Action {
			case tea.MouseActionPress:
				// Clear any previous selection before starting a new one
				m.clearCharSelection()
				pos := posFromCoord(contentLine, contentCol, m.lineSrcs)
				if pos.Offset >= 0 {
					m.charSelStart = pos
					m.charSelEnd = pos
					m.charSelStartLine = contentLine
					m.charSelStartCol = contentCol
					m.charSelEndLine = contentLine
					m.charSelEndCol = contentCol
					m.selecting = true
				}
			case tea.MouseActionMotion:
				if m.selecting {
					pos := posFromCoord(contentLine, contentCol, m.lineSrcs)
					if pos.Offset >= 0 {
						m.charSelEnd = pos
						m.charSelEndLine = contentLine
						m.charSelEndCol = contentCol
					}
				}
			case tea.MouseActionRelease:
				m.selecting = false
			}

			// Legacy message-level selection (old path — kept for backward compat)
			idx := m.messageAtLine(contentLine)
			if idx < 0 {
				idx = 0
			}
			if idx >= len(m.messages) {
				idx = len(m.messages) - 1
			}
			switch msg.Action {
			case tea.MouseActionPress:
				m.mouseDrag = false
				m.selecting = true
				m.selectStart = idx
				m.selectEnd = idx
				roleStr := ""
				if idx >= 0 && idx < len(m.messages) {
					roleStr = m.messages[idx].Role
				}
				tlog.Debug("mouse.select", "press",
					"y", msg.Y, "contentLine", contentLine,
					"yOffset", m.vp.YOffset, "msgIdx", idx,
					"msgRole", roleStr)
			case tea.MouseActionMotion:
				if m.selecting {
					m.mouseDrag = true
					m.selectEnd = idx
					tlog.Debug("mouse.select", "drag",
						"y", msg.Y, "contentLine", contentLine,
						"msgIdx", idx, "range", fmt.Sprintf("[%d,%d]", m.selectStart, m.selectEnd))
				}
			case tea.MouseActionRelease:
				if !m.mouseDrag {
					// Click without drag: clear any previous selection
					m.clearCharSelection()
					m.selectStart = -1
					m.selectEnd = -1
					clickRole := ""
					if idx >= 0 && idx < len(m.messages) {
						clickRole = m.messages[idx].Role
					}
					tlog.Debug("mouse.select", "click",
						"y", msg.Y, "contentLine", contentLine,
						"msgIdx", idx, "msgRole", clickRole,
						"action", "cleared")
				} else {
					// Drag release: keep selection highlighted for copy
					tlog.Debug("mouse.select", "release",
						"y", msg.Y, "contentLine", contentLine,
						"msgIdx", idx, "range", fmt.Sprintf("[%d,%d]", m.selectStart, m.selectEnd))
				}
				m.selecting = false
			}
			return m, nil
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if !m.ready {
			m.vp = viewport.New(msg.Width, msg.Height-1-m.input.Height())
			m.vp.YPosition = 0
			m.ready = true
		} else {
			m.vp.Width = msg.Width
			m.vp.Height = msg.Height - 1 - m.input.Height()
		}
		m.input.SetWidth(msg.Width - 4)
		return m, nil

	case tea.KeyMsg:
		if m.selectingProvider {
			switch msg.Type {
			case tea.KeyUp:
				if m.providerCursor > 0 {
					m.providerCursor--
				}
			case tea.KeyDown:
				if m.providerCursor < m.provReg.Len()-1 {
					m.providerCursor++
				}
			case tea.KeyEnter:
				idx := m.providerCursor
				if idx >= 0 && idx < m.provReg.Len() {
					if err := m.provReg.SwitchTo(idx); err == nil {
						m.agent.Provider = m.provReg.Current()
						m.ShowStatus(m.provReg.CurrentName())
					}
				}
				m.selectingProvider = false
				return m, nil
			case tea.KeyEscape, tea.KeyCtrlC:
				m.selectingProvider = false
				return m, nil
			}
			return m, nil
		}

		// Escape → exit history browsing
		// Command palette active → intercept before history
		if m.cmdPalette {
			cmds := m.filteredCmds()
			switch msg.Type {
			case tea.KeyUp:
				if m.cmdPaletteSel > 0 {
					m.cmdPaletteSel--
				}
				return m, nil
			case tea.KeyDown:
				if m.cmdPaletteSel < len(cmds)-1 {
					m.cmdPaletteSel++
				}
				return m, nil
			case tea.KeyEnter:
				if len(cmds) > 0 {
					cmd := cmds[m.cmdPaletteSel].Name
					m.cmdPalette = false
					m.cmdPaletteInput = ""
					m.cmdPaletteSel = 0
					m.input.SetValue(cmd)
					m.input.SetCursor(len(cmd))
					return m, nil
				}
			case tea.KeyEscape, tea.KeyCtrlC:
				m.cmdPalette = false
				m.cmdPaletteInput = ""
				m.cmdPaletteSel = 0
				m.input.SetValue("")
				return m, nil
			}
			// Typing — update textarea and sync filter
			m.input, _ = m.input.Update(msg)
			val := strings.TrimPrefix(strings.TrimLeft(m.input.Value(), "/"), " ")
			m.cmdPaletteInput = val
			m.cmdPaletteSel = 0
			if val == "" {
				m.cmdPalette = false
			}
			return m, nil
		}

		// Dialog mode — intercept keyboard before normal input
		if m.dialogMode {
			return m.handleDialogKey(msg), nil
		}

		// Auto-show permission dialog when sandbox blocks a path
		if tool.HasPendingPermission() && !m.dialogMode {
			m.checkPermissionDialog()
		}

		// Escape → exit history browsing
		if msg.Type == tea.KeyEscape && m.historyPos >= 0 {
			m.historyPos = -1
			m.input.SetValue(m.historyDraft)
			return m, nil
		}

		// "/" on empty input → activate command palette
		if msg.Type == tea.KeyRunes && string(msg.Runes) == "/" && m.input.Value() == "" {
			m.cmdPalette = true
			m.cmdPaletteInput = ""
			m.cmdPaletteSel = 0
		}

		// Up/Down → browse input history
		if msg.Type == tea.KeyUp && len(m.inputHistory) > 0 {
			if m.historyPos < 0 {
				// Save draft before browsing
				m.historyDraft = m.input.Value()
				m.historyPos = len(m.inputHistory) - 1
			} else if m.historyPos > 0 {
				m.historyPos--
			}
			m.input.SetValue(m.inputHistory[m.historyPos])
			return m, nil
		}
		if msg.Type == tea.KeyDown {
			if m.historyPos >= 0 {
				if m.historyPos < len(m.inputHistory)-1 {
					m.historyPos++
					m.input.SetValue(m.inputHistory[m.historyPos])
				} else {
					// Back to draft
					m.historyPos = -1
					m.input.SetValue(m.historyDraft)
				}
				return m, nil
			}
			// Not browsing history — let Down pass through to textarea
		}

		// Tab → mode switch
		if msg.Type == tea.KeyTab {
			return m, func() tea.Msg { return modeSwitchMsg{} }
		}

		// Enter → submit
		if msg.Type == tea.KeyEnter && !msg.Alt {
			if m.status != StatusStreaming && !m.runIsActive() && strings.TrimSpace(m.input.Value()) != "" {
				return m.submitInput()
			}
		}

		// Ctrl+J → newline
		if msg.Type == tea.KeyCtrlJ {
			m.input.SetValue(m.input.Value() + "\n")
			m.adjustInputHeight()
			return m, nil
		}

		// Ctrl+C: copy (char or msg selection) | interrupt | double-tap quit
		if msg.Type == tea.KeyCtrlC {
			// Always clear stale selection state on Ctrl+C
			m.selecting = false
			// Character-level selection copy using CellGrid coordinates
			// Only copy if the selection spans at least one character (start != end)
			if m.charSelStart.Offset >= 0 && m.charSelEnd.Offset >= 0 && m.grid != nil &&
				(m.charSelStartLine != m.charSelEndLine || m.charSelStartCol != m.charSelEndCol) {
				text := m.grid.ExtractText(
					m.charSelStartLine, m.charSelStartCol,
					m.charSelEndLine, m.charSelEndCol,
				)
				if text != "" {
					text = strings.TrimSpace(text)
				}
				if text != "" {
					copyToClipboard(text)
					m.clearCharSelection()
					m.ShowStatus("✓ Copied")
					m.autoScroll()
					return m, nil
				}
			}
			// Message-level selection copy (fallback)
			if sel := m.selectedMessages(); sel != "" {
				tlog.Debug("ctrl-c", "path", "msg", "select_start", m.selectStart, "select_end", m.selectEnd)
				copyToClipboard(sel)
				m.selectStart = -1
				m.selectEnd = -1
				m.ShowStatus("✓ Copied to clipboard")
				m.autoScroll()
				return m, nil
			}
			if m.status == StatusStreaming {
				switch {
				case m.runAlreadyInterrupted():
					// Cancellation already requested; wait for the run to unwind.
					m.ShowStatus("⏹ Interrupting...")
				case m.cancelRun():
					// A live run owns the status now: it flips back to Idle
					// only when its terminal StreamDone is processed.
					// Unblock any permission dialog the run is waiting on.
					tool.CancelPendingPermission()
					m.ShowStatus("⏹ Interrupted")
				default:
					// No live run attached (state set directly): reset here.
					m.status = StatusIdle
					m.ShowStatus("⏹ Interrupted")
				}
				m.autoScroll()
				return m, nil
			}
			if !m.quitConfirm {
				m.quitConfirm = true
				m.statusMsg = "Press Ctrl+C again to quit"
				m.autoScroll()
				return m, nil
			}
			// Save session before quitting
			m.persistSession()
			return m, tea.Quit
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.adjustInputHeight()
		return m, cmd

	case spinner.TickMsg:
		// Always advance spinner and keep tick pipeline alive
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.status == StatusStreaming {
			return m, cmd
		}
		// Keep ticks flowing even when idle so spinner is ready when streaming starts
		return m, cmd

	case ChatMsg:
		// Refuse a second concurrent run: the previous run may still be
		// unwinding (e.g. right after an interrupt).
		ctx, runID, ok := m.beginRun()
		if !ok {
			m.ShowStatus("A run is already in progress")
			m.autoScroll()
			return m, nil
		}
		m.messages = append(m.messages, chatMessage{Role: "user", Content: msg.Text})
		cur := chatMessage{Role: "assistant", Streaming: true}
		m.messages = append(m.messages, cur)
		m.curAssistant = &m.messages[len(m.messages)-1]
		m.status = StatusStreaming
		m.streamDoneNotified = false
		go m.runAgent(ctx, runID, msg.Text)
		return m, m.waitForStream()

	case ToolCallMsg:
		if m.isStaleRun(msg.RunID) {
			return m, m.waitForStream()
		}
		count := 0
		if m.curAssistant != nil {
			m.curAssistant.ToolCalls = append(m.curAssistant.ToolCalls, ToolCallInfo{
				Name: msg.Name,
				Arg:  msg.Arg,
			})
			count = len(m.curAssistant.ToolCalls)
		}
		// One reported tool call counts, even if it cannot be attached to a
		// message (e.g. a late message after the assistant was finalized).
		m.sessionToolCalls++
		tlog.Debug("toolcall.msg", "name", msg.Name, "arg", msg.Arg, "count", count)
		m.autoScroll()
		return m, m.waitForStream()

	case ToolResultMsg:
		if m.isStaleRun(msg.RunID) {
			// Unblock a run that is waiting on the render ack even though its
			// output is no longer displayed.
			if msg.AckCh != nil {
				msg.AckCh <- struct{}{}
			}
			return m, m.waitForStream()
		}
		// If the completed tool was "todo", mark dirty and save the render
		// ack channel. The agent goroutine blocks on this channel; View()
		// signals it after the TODO is injected into the CellGrid.
		if msg.Name == "todo" {
			m.todoDirty = true
			m.renderAckCh = msg.AckCh
		}
		m.autoScroll()
		return m, m.waitForStream()
	case LSPDiagMsg:
		m.diagTotal = msg.Count
		m.diagFile = msg.FilePath
		m.autoScroll()
		return m, nil
	case StreamMsg:
		if m.isStaleRun(msg.RunID) {
			return m, m.waitForStream()
		}
		if m.curAssistant == nil {
			return m, m.waitForStream()
		}
		// Approximate token accounting. The running total is derived from the
		// accumulated message text (like agent.EstimateTokens) rather than the
		// delta length, so character-by-character streaming is counted too.
		prevTokens := agent.EstimateTokens(m.curAssistant.Content) +
			agent.EstimateTokens(m.curAssistant.ReasoningContent)
		if msg.ReasoningDelta != "" {
			m.curAssistant.ReasoningContent += msg.ReasoningDelta
		}
		if msg.TextDelta != "" {
			m.curAssistant.Content += msg.TextDelta
		}
		curTokens := agent.EstimateTokens(m.curAssistant.Content) +
			agent.EstimateTokens(m.curAssistant.ReasoningContent)
		if curTokens > prevTokens {
			m.sessionTokens += curTokens - prevTokens
		}
		// Mark the streaming message as dirty so View() re-renders it
		if len(m.msgDirty) > 0 {
			m.msgDirty[len(m.msgDirty)-1] = true
		}
		m.autoScroll()
		return m, m.waitForStream()

	case StreamDone:
		// Ignore output from a superseded run while a newer run is active.
		if m.isStaleRun(msg.RunID) {
			return m, m.waitForStream()
		}
		ca := m.curAssistant
		// Check if this is an intermediate step (more to come) or the final
		// response. An error always terminates the run, even when the message
		// carries tool calls from the step that failed.
		isIntermediate := msg.IsIntermediate || (msg.Error == nil && ca != nil && len(ca.ToolCalls) > 0)
		if msg.Error != nil {
			if ca != nil {
				if errors.Is(msg.Error, context.Canceled) {
					ca.Content = "⏹ Interrupted"
				} else {
					ca.Content = fmt.Sprintf("Error: %v", msg.Error)
				}
				ca.Streaming = false
			}
			if errors.Is(msg.Error, context.Canceled) {
				m.ShowStatus("⏹ Interrupted")
			}
		} else if msg.IsIntermediate {
			// Intermediate step: just finalize, don't set Blocks (no text response)
			if ca != nil {
				ca.Streaming = false
			}
		} else {
			if ca != nil {
				ca.Streaming = false
				// Mute if all tool calls are housekeeping (todo, memory, etc.)
				housekeeping := map[string]bool{"todo": true, "memory": true}
				if len(ca.ToolCalls) > 0 {
					allHousekeeping := true
					for _, tc := range ca.ToolCalls {
						if !housekeeping[tc.Name] {
							allHousekeeping = false
							break
						}
					}
					if allHousekeeping {
						ca.ReasoningContent = ""
					}
				}
				if msg.Content != "" {
					ca.Blocks = parseMarkdown(msg.Content)
				}
			}
		}
		// Mark dirty so View() re-renders the message with [Copy] button
		if len(m.msgDirty) > 0 {
			m.msgDirty[len(m.msgDirty)-1] = true
		}
		// Mark todo dirty so CellGrid re-renders todo in-place
		m.todoDirty = true
		// Take TODO snapshot for this message (at StreamDone time)
		if ca != nil && m.todoStore != nil {
			ca.TodoSnapshot = m.todoStore.Read()
		}
		m.streamDoneNotified = false
		m.autoScroll()
		// If this is an intermediate step, prepare a new assistant message for
		// the next step and keep draining the stream.
		if isIntermediate {
			cur := chatMessage{Role: "assistant", Streaming: true}
			m.messages = append(m.messages, cur)
			m.curAssistant = &m.messages[len(m.messages)-1]
			return m, m.waitForStream()
		}
		// Terminal step: the run has actually reported completion, so it is
		// now safe to leave the streaming state and accept new input.
		m.finishRun(msg.RunID)
		m.status = StatusIdle
		m.curAssistant = nil
		if msg.Error != nil {
			// Do not spend an LLM call on a title after a failed or
			// interrupted run.
			return m, nil
		}
		// Generate the session title off the event loop (async + timeout).
		return m, m.generateSessionTitleCmd()

	case sessionTitleMsg:
		if msg.Title != "" && m.sessionTitle == "" {
			m.sessionTitle = msg.Title
		}
		return m, nil

	case modeSwitchMsg:
		m.registry.Switch()
		m.agent.Config = m.registry.Current()
		m.modeName = m.registry.CurrentName()

		m.autoScroll()
		return m, nil
	}

	return m, nil
}

const maxInputHeight = 10

func (m *TuiModel) adjustInputHeight() {
	lines := m.input.LineCount()
	wanted := lines
	if wanted < 1 {
		wanted = 1
	}
	if wanted > maxInputHeight {
		wanted = maxInputHeight
	}
	if m.input.Height() != wanted {
		val := m.input.Value()
		m.input.SetHeight(wanted)
		m.input.SetValue(val)
		if m.ready {
			m.vp.Height = m.height - 1 - wanted
		}
	}
}

// messageAtLine returns the message index for a given content line number.
func (m *TuiModel) messageAtLine(contentLine int) int {
	if contentLine < 0 || len(m.messages) == 0 {
		return 0
	}
	termW := m.width - 6
	if termW < 20 {
		termW = 20
	}
	line := 0
	for i, msg := range m.messages {
		var n int
		switch msg.Role {
		case "user":
			n = 1
		case "system":
			n = 1
		case "assistant":
			if msg.ReasoningContent != "" {
				n += visibleLines(msg.ReasoningContent, termW)
			}
			n += 1
			if msg.Content != "" && (m.curAssistant == nil || m.curAssistant.Content != "") {
				n += visibleLines(msg.Content, termW)
			}
		}
		tlog.Trace("mouse.select", "messageAtLine",
			"i", i, "role", msg.Role,
			"n", n, "line_start", line, "line_end", line+n-1,
			"width", m.width, "termW", termW,
			"contentLen", len(msg.Content), "reasoningLen", len(msg.ReasoningContent))
		if contentLine < line+n {
			return i
		}
		line += n
	}
	return -1
}

// visibleLines estimates terminal lines occupied, using lipgloss.Width
// to skip ANSI escape codes and handle wide chars (emoji, CJK).
func visibleLines(s string, termW int) int {
	if s == "" || termW < 1 {
		return 0
	}
	lines := 0
	for _, line := range strings.Split(s, "\n") {
		w := lipgloss.Width(line)
		if w == 0 {
			lines++
		} else {
			lines += (w + termW - 1) / termW
		}
	}
	if lines == 0 {
		lines = 1
	}
	return lines
}

func (m *TuiModel) isSelected(i int) bool {
	if m.selectStart < 0 {
		return false
	}
	start, end := m.selectStart, m.selectEnd
	if end < start {
		start, end = end, start
	}
	return i >= start && i <= end
}

func (m *TuiModel) selectedMessages() string {
	start, end := m.selectStart, m.selectEnd
	if start < 0 {
		return ""
	}
	if end < start {
		start, end = end, start
	}
	var b strings.Builder
	for i := start; i <= end && i < len(m.messages); i++ {
		if m.messages[i].Role == "assistant" {
			if m.messages[i].ReasoningContent != "" {
				b.WriteString(m.messages[i].ReasoningContent)
				b.WriteString("\n\n")
			}
		}
		content := m.messages[i].Content
		if m.messages[i].Role == "user" {
			content = "> " + content
		} else if m.messages[i].Role == "system" {
			content = "→ " + content
		}
		if content != "" {
			b.WriteString(content)
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// copyToClipboard writes text to the system clipboard via OSC 52 escape sequence.
func copyToClipboard(text string) {
	if text == "" {
		return
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	fmt.Printf("\033]52;c;%s\007", encoded)
}

// beginRun registers a new agent run and returns its context plus generation
// id. ok is false when a run is already active, so the caller must not start
// another one.
func (m *TuiModel) beginRun() (context.Context, uint64, bool) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	if m.runActive {
		return nil, m.runID, false
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.runID++
	m.runActive = true
	m.runCancel = cancel
	m.runInterrupted = false
	return ctx, m.runID, true
}

// finishRun marks the run with the given id as finished. A stale id (a
// superseded run) is ignored so it cannot clear a newer run's state.
func (m *TuiModel) finishRun(id uint64) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	if !m.runActive || m.runID != id {
		return
	}
	m.runActive = false
	m.runCancel = nil
	m.runInterrupted = false
}

// cancelRun cancels the active run's context. It reports whether a live run
// was actually cancelled.
func (m *TuiModel) cancelRun() bool {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	if !m.runActive || m.runCancel == nil {
		return false
	}
	m.runCancel()
	m.runInterrupted = true
	return true
}

// runIsActive reports whether an agent run is currently in flight.
func (m *TuiModel) runIsActive() bool {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	return m.runActive
}

// runAlreadyInterrupted reports whether the active run has been cancelled by
// the user and is still unwinding.
func (m *TuiModel) runAlreadyInterrupted() bool {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	return m.runActive && m.runInterrupted
}

// isStaleRun reports whether a stream message belongs to a superseded run.
// While no run is active nothing is treated as stale, which keeps direct
// Update calls (tests, late messages after completion) working.
func (m *TuiModel) isStaleRun(id uint64) bool {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	return m.runActive && id != m.runID
}

// runIsCurrent reports whether the given generation id still owns the model.
// The agent goroutine uses it to avoid injecting output after being superseded.
func (m *TuiModel) runIsCurrent(id uint64) bool {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	return m.runActive && id == m.runID
}

// persistSession writes the conversation to disk. It reuses the resumed or
// forked session id when one is set, so quitting updates the current session
// instead of duplicating it; otherwise it mints a new TUI-<timestamp> id.
func (m *TuiModel) persistSession() {
	if m.SessionDir == "" || len(m.messages) == 0 {
		return
	}
	id := m.currentBranch
	if id == "" {
		id = "TUI-" + time.Now().Format("20060102-150405")
		m.currentBranch = id
	}
	var s *session.Session
	if existing, err := session.Load(id, m.SessionDir); err == nil {
		// Reuse the stored session so metadata such as AllowedPaths survives,
		// but rebuild the message list from the live transcript.
		s = existing
		s.Messages = nil
	} else {
		s = session.New(id, m.SessionDir)
	}
	for _, chatMsg := range m.messages {
		s.Append(types.Message{
			Role:             chatMsg.Role,
			Content:          chatMsg.Content,
			ReasoningContent: chatMsg.ReasoningContent,
		})
	}
	if m.provReg != nil {
		s.ModelName = m.provReg.Current().Name()
	}
	if m.sessionTitle != "" {
		s.Title = m.sessionTitle
	}
	if err := s.Flush(); err != nil {
		tlog.Warn("tui.session", "flush_error", "err", err)
	}
}

// saveBranchSession persists the current branch's messages to disk.
func (m *TuiModel) saveBranchSession() {
	if m.currentBranch == "" {
		return
	}
	m.persistSession()
}

// autoScroll scrolls to bottom only if user is already at the bottom.
func (m *TuiModel) autoScroll() {
	if m.ready && m.vp.AtBottom() {
		m.vp.GotoBottom()
	}
}

// sessionTitleTimeout bounds the background title-generation LLM call.
// It is a variable so tests can shorten it.
var sessionTitleTimeout = 15 * time.Second

// generateSessionTitleCmd returns a command that derives a session title with
// the hidden "title" agent. The LLM call runs off the event loop with a
// timeout so a slow provider cannot freeze the TUI. The prompt is built and
// the fallback computed before returning, so the command goroutine never
// touches model state.
func (m *TuiModel) generateSessionTitleCmd() tea.Cmd {
	if m.agent == nil || m.agent.Provider == nil || m.registry == nil ||
		len(m.messages) < 2 || m.sessionTitle != "" {
		return nil
	}
	fallback := extractFirstUserMsg(m.messages)
	cfg, err := m.registry.Get("title")
	if err != nil {
		return func() tea.Msg { return sessionTitleMsg{Title: fallback} }
	}
	var b strings.Builder
	b.WriteString(cfg.SystemPrompt)
	b.WriteString("\n\nConversation so far:\n")
	for _, msg := range m.messages {
		content := msg.Content
		if len(content) > 200 {
			content = content[:200] + "..."
		}
		b.WriteString(fmt.Sprintf("%s: %s\n", msg.Role, content))
	}
	prompt := b.String()
	provider := m.agent.Provider
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), sessionTitleTimeout)
		defer cancel()
		resp, err := provider.Chat(ctx, types.ChatRequest{
			Messages: []types.Message{
				{Role: types.RoleUser, Content: prompt},
			},
		})
		if err != nil || resp == nil || resp.Content == "" {
			return sessionTitleMsg{Title: fallback}
		}
		title := strings.TrimSpace(resp.Content)
		if len(title) > 80 {
			title = title[:80]
		}
		return sessionTitleMsg{Title: title}
	}
}

// extractFirstUserMsg returns the first user message truncated for use as a title.
func extractFirstUserMsg(msgs []chatMessage) string {
	for _, msg := range msgs {
		if msg.Role == "user" && msg.Content != "" {
			text := strings.ReplaceAll(msg.Content, "\n", " ")
			if len(text) > 80 {
				text = text[:80] + "..."
			}
			return text
		}
	}
	return "untitled"
}

// submitInput handles user text input (slash commands or normal messages).
func (m *TuiModel) submitInput() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	// Save to input history (dedup last entry)
	if len(m.inputHistory) == 0 || m.inputHistory[len(m.inputHistory)-1] != text {
		m.inputHistory = append(m.inputHistory, text)
	}
	m.historyPos = -1
	m.historyDraft = ""
	m.lastInput = text
	m.input.Reset()
	if strings.HasPrefix(text, "/") {
		return m.handleCommand(text)
	}
	return m, func() tea.Msg { return ChatMsg{Text: text} }
}

func (m *TuiModel) handleCommand(cmd string) (tea.Model, tea.Cmd) {
	// Extract base command (first word) for switch matching
	parts := strings.Fields(cmd)
	base := cmd
	if len(parts) > 0 {
		base = parts[0]
	}
	switch base {
	case "/exit", "/quit":
		m.persistSession()
		return m, tea.Quit
	case "/compress":
		if m.agent == nil {
			m.ShowStatus("No agent available")
			return m, nil
		}
		// CompressHistory replaces a.History while the agent goroutine may be
		// appending to it, so it must never run concurrently with an active
		// run. Refusing while runIsActive() is sufficient: the run lifecycle
		// (beginRun/finishRun) clears runActive only when the agent goroutine
		// has delivered its terminal StreamDone, so once no run is active no
		// goroutine is touching History anymore.
		if m.runIsActive() {
			m.ShowStatus("Cannot compress: a run is in progress")
			return m, nil
		}
		if m.agent.CompressionThreshold <= 0 {
			m.ShowStatus("Compression is disabled (CompressionThreshold=0)")
			return m, nil
		}
		if m.agent.CompressHistory() {
			m.ShowStatus(fmt.Sprintf("Compressed: %d messages remaining", len(m.agent.History)))
		} else {
			m.ShowStatus(fmt.Sprintf("No compression needed (%d messages, below threshold)", len(m.agent.History)))
		}
		return m, nil

	case "/dialog":
		m.showDialog("Test dialog — choose an option:", []string{
			"Option One",
			"Option Two",
			"Option Three",
		}, func(sel string) {
			m.messages = append(m.messages, chatMessage{
				Role:    "system",
				Content: "Dialog selected: " + sel,
			})
		})
		return m, nil

	case "/help":
		help := `Available commands and shortcuts:

Commands:
  /help          Show this help
  /exit, /quit   Exit TinyCode
  /plan          Switch to plan mode
  /build         Switch to build mode
  /compress      Manually trigger conversation compression
  /model         Switch provider/model
  /sessions      List saved sessions
  /verbose       Toggle verbose output
  /thinking      Toggle thinking display
  /skill         Load a skill's instructions (e.g. /skill code-review)
  /theme         Switch theme (nord, default)
  /diagnostics   Show LSP diagnostics
  /model         Switch provider/model

Keyboard:
  Enter          Submit message
  Ctrl+J         New line (in textarea)
  Ctrl+C         Copy selected text / Interrupt streaming / Quit
  ↑ / ↓          Browse input history
  Esc            Exit history browsing
  Tab            Switch modes (when input is empty)

Mouse:
  Click + drag    Select text range
  [Copy] button   Copy assistant message to clipboard
  [−] / [+]      Expand/collapse reasoning block`
		m.messages = append(m.messages, chatMessage{Role: "system", Content: help})
		m.autoScroll()
	case "/verbose":
		m.agent.Verbose = !m.agent.Verbose
		s := "off"
		if m.agent.Verbose {
			s = "on"
		}
		m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Verbose mode %s", s)})
		m.autoScroll()
	case "/fork":
		parts := strings.Fields(cmd)
		label := ""
		if len(parts) > 1 {
			label = parts[1]
		}
		if m.currentBranch == "" {
			m.messages = append(m.messages, chatMessage{Role: "system", Content: "No active session to fork. Start a conversation first."})
			m.autoScroll()
			return m, nil
		}
		branch, err := m.sessionStore.Fork(m.currentBranch, len(m.messages), label)
		if err != nil {
			m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Fork error: %v", err)})
			m.autoScroll()
			return m, nil
		}
		m.messages = nil
		m.messages = append(m.messages, chatMessage{
			Role:    "system",
			Content: fmt.Sprintf("Created branch: %s (forked at message %d from %s)", branch.ID, branch.ForkAt, branch.ParentSessionID),
		})
		m.currentBranch = branch.ID
		m.resetSessionStats()
		m.autoScroll()
	case "/session":
		parts := strings.Fields(cmd)
		if len(parts) < 2 {
			// List all branches/sessions
			infos := m.sessionStore.List()
			var sb strings.Builder
			sb.WriteString("Sessions and branches:\n")
			for _, info := range infos {
				mark := "  "
				if info.ID == m.currentBranch {
					mark = " *"
				}
				title := info.Title
				if title == "" {
					title = "(no title)"
				}
				msgs := info.MessageCount
				sb.WriteString(fmt.Sprintf("%s %-40s %s (%d msgs)\n", mark, info.ID, title, msgs))
			}
			m.messages = append(m.messages, chatMessage{Role: "system", Content: strings.TrimSpace(sb.String())})
			m.autoScroll()
			return m, nil
		}
		// Switch to specified branch
		branchID := parts[1]
		branch, err := m.sessionStore.Load(branchID)
		if err != nil {
			m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Session %q not found.", branchID)})
			m.autoScroll()
			return m, nil
		}
		// Save current session first
		if m.currentBranch != "" {
			m.saveBranchSession()
		}
		// Load branch messages
		m.messages = nil
		for _, sm := range branch.Messages {
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
		m.currentBranch = branch.ID
		m.resetSessionStats()
		m.messages = append(m.messages, chatMessage{
			Role:    "system",
			Content: fmt.Sprintf("Switched to session: %s (%d messages)", branch.ID, len(branch.Messages)),
		})
		m.autoScroll()
	case "/thinking":
		if m.config.ShowThinking == nil {
			v := false
			m.config.ShowThinking = &v
		}
		*m.config.ShowThinking = !*m.config.ShowThinking
		s := "off"
		if *m.config.ShowThinking {
			s = "on"
		}
		m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Thinking display %s", s)})
		m.autoScroll()
	case "/model":
		m.selectingProvider = true
		m.providerCursor = 0
	case "/sessions":
		infos := m.sessionStore.List()
		if len(infos) == 0 {
			m.messages = append(m.messages, chatMessage{Role: "system", Content: "No saved sessions."})
		} else {
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("**%d saved sessions:**\n\n", len(infos)))
			sb.WriteString("| Title | ID | Messages | Model | Last active |\n")
			sb.WriteString("|-------|-----|----------|-------|-------------|\n")
			for _, info := range infos {
				title := info.Title
				if title == "" {
					title = "(no title)"
				}
				model := info.ModelName
				if model == "" {
					model = "?"
				}
				when := info.UpdatedAt.Format("2006-01-02 15:04")
				sb.WriteString(fmt.Sprintf("| %s | `%s` | %d | %s | %s |\n",
					title, info.ID, info.MessageCount, model, when))
			}
			sb.WriteString(fmt.Sprintf("\nUse `%s --resume <ID>` (CLI) to resume a session.", os.Args[0]))
			m.messages = append(m.messages, chatMessage{Role: "system", Content: sb.String()})
		}
		m.autoScroll()
		return m, nil
	case "/theme":
		parts := strings.Fields(cmd)
		if len(parts) < 2 {
			names := strings.Join(ThemeNames(), ", ")
			m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Available themes: %s. Use /theme <name> to switch.", names)})
			m.autoScroll()
			return m, nil
		}
		theme := LookupTheme(parts[1])
		if theme == nil {
			m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Unknown theme %q. Available: %s", parts[1], strings.Join(ThemeNames(), ", "))})
			m.autoScroll()
			return m, nil
		}
		ApplyTheme(*theme)
		m.config.Theme = theme.Name
		if err := m.config.Save(); err != nil {
			tlog.Error("theme.save", "error", err)
		}
		m.MarkAllDirty()
		m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Switched to theme: %s", theme.Name)})
		m.autoScroll()
	case "/diagnostics":
		if !lsp.IsAvailable() {
			m.ShowStatus("LSP not available (set lsp.enabled=true in config.json)")
		} else if m.diagTotal == 0 {
			m.ShowStatus("No LSP diagnostics.")
		} else {
			m.ShowStatus(fmt.Sprintf("%d LSP errors in %s", m.diagTotal, m.diagFile))
		}
		return m, nil
	case "/skill":
		parts := strings.Fields(cmd)
		skills := skill.Discover(".")
		if len(parts) < 2 {
			var b strings.Builder
			b.WriteString(fmt.Sprintf("Available skills (%d):\n", len(skills)))
			for _, s := range skills {
				label := ""
				if s.Builtin {
					label = " [builtin]"
				}
				b.WriteString(fmt.Sprintf("  %s — %s%s\n", s.Name, s.Description, label))
			}
			b.WriteString("\nUse /skill <name> to load, or the load_skill tool.")
			m.messages = append(m.messages, chatMessage{Role: "system", Content: b.String()})
			m.autoScroll()
			return m, nil
		}
		name := parts[1]
		var found *skill.Skill
		for i := range skills {
			if skills[i].Name == name {
				found = &skills[i]
				break
			}
		}
		if found == nil {
			m.ShowStatus(fmt.Sprintf("Skill not found: %s", name))
			return m, nil
		}
		// An explicit /skill command always shows the content. The
		// once-per-session dedup in LoadOnce exists to keep the load_skill
		// *tool* from re-injecting the same instructions into the model
		// context, not to hide content the user asked for again.
		content := skill.LoadContent(name, ".")
		if content == "" {
			content = found.Description
		}
		msg := "Loaded skill: " + name + "\n\n" + content
		m.messages = append(m.messages, chatMessage{
			Role:    "system",
			Content: msg,
		})
		m.autoScroll()
		return m, nil
	case "/plan":
		if err := m.registry.Set("plan"); err != nil {
			m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Error: %v", err)})
			m.autoScroll()
			return m, nil
		}
		m.agent.Config = m.registry.Current()
		m.modeName = "plan"
		m.autoScroll()
	case "/build":
		if err := m.registry.Set("build"); err != nil {
			m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Error: %v", err)})
			m.autoScroll()
			return m, nil
		}
		m.agent.Config = m.registry.Current()
		m.modeName = "build"
		m.autoScroll()
	default:
		m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Unknown command: %s", cmd)})
		m.autoScroll()
	}
	return m, nil
}

func (m *TuiModel) waitForStream() tea.Cmd {
	return func() tea.Msg {
		return <-m.streamCh
	}
}

func (m *TuiModel) runAgent(ctx context.Context, runID uint64, prompt string) {
	m.agent.StreamCallbacks = &types.StreamCallbacks{
		OnReasoningDelta: func(text string) {
			m.streamCh <- StreamMsg{RunID: runID, ReasoningDelta: text}
		},
		OnTextDelta: func(text string) {
			m.streamCh <- StreamMsg{RunID: runID, TextDelta: text}
		},
		OnToolCall: func(name, arg string) {
			m.streamCh <- ToolCallMsg{RunID: runID, MsgIdx: -1, Name: name, Arg: arg}
		},
		OnToolResult: func(name string) {
			var ackCh chan struct{}
			if name == "todo" {
				ackCh = make(chan struct{}, 1)
			}
			m.streamCh <- ToolResultMsg{RunID: runID, MsgIdx: -1, Name: name, AckCh: ackCh}
			if ackCh != nil {
				<-ackCh // BLOCK until View() injects TODO into CellGrid
			}
		},
		OnStepDone: func() {
			m.streamCh <- StreamDone{RunID: runID, IsIntermediate: true}
		},
	}
	result, err := m.agent.Run(ctx, prompt)
	m.agent.StreamCallbacks = nil
	if !m.runIsCurrent(runID) {
		// The run was superseded (or already finalized): do not inject output
		// that would corrupt the newer run's transcript.
		return
	}
	m.streamCh <- StreamDone{
		RunID:   runID,
		Content: result,
		Error:   err,
	}
}
