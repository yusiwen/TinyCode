package tui

import (
	"fmt"
	"time"

	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/yusiwen/tinycode/goal"
)

// View renders the TUI layout.
func (m *TuiModel) View() string {
	if !m.ready {
		return "Loading..."
	}

	var b strings.Builder

	// Message area — built with CellGrid
	// activeButtons is rebuilt per message during rendering.
	// Buttons for non-dirty messages (not re-rendered) must be preserved,
	// so we do NOT nil here — instead, each rendered message clears its own old buttons.
	// See the rendering loop below.
	m.ensureMsgTracking()

	// Find first dirty message (recalc start row from previous messages' rowCount)
	firstDirty := -1
	for i := range m.messages {
		if m.msgDirty[i] {
			firstDirty = i
			break
		}
	}

	if m.grid == nil || m.grid.width != gridWidth(m.vp.Width) {
		// Resize: full rebuild. Compare against the clamped width: comparing
		// against a raw 0 would rebuild every frame for a zero-width viewport.
		m.grid = NewCellGrid(m.vp.Width, 10)
		m.lineSrcs = nil
		m.MarkAllDirty()
		firstDirty = 0
	}

	g := m.grid

	if firstDirty < 0 && !m.todoDirty {
		// Nothing changed — keep existing grid, don't rebuild. Nothing to do.
		// lineSrcs from previous frame are still valid.
	} else {
		// When only todo is dirty, re-render only the last message
		if firstDirty < 0 && m.todoDirty && len(m.messages) > 0 {
			firstDirty = len(m.messages) - 1
		}
		// Guard: with no messages to render (firstDirty is still -1) start the
		// render loop past the end. Leaving it at -1 would index m.messages[-1]
		// below. The todo flag is still cleared by the ack block at the end of
		// View, so the next render starts from a consistent state.
		if firstDirty < 0 {
			firstDirty = len(m.messages)
		}

		// Compute the grid row where firstDirty starts. Only messages that
		// have a renderer contribute rows and an inter-message blank line, so
		// the accounting below mirrors the render loop exactly.
		dirtyStart := 0
		renderedBefore := false
		for j := 0; j < firstDirty; j++ {
			if !hasMsgComponent(m.messages[j].Role) {
				continue
			}
			if renderedBefore {
				dirtyStart++ // inter-message blank line
			}
			renderedBefore = true
			dirtyStart += m.msgRowCount[j]
		}

		// Truncate grid: set g.row back to dirtyStart
		g.row = dirtyStart
		g.col = 0
		// Clear cells from dirtyStart onwards
		for r := dirtyStart; r < g.rows; r++ {
			for c := 0; c < g.width; c++ {
				g.cells[g.cellIndex(r, c)] = Cell{}
			}
		}

		// Truncate lineSrcs back. lineSrcs holds one entry per rendered row
		// (blank separators are not recorded), so it must match the row counts
		// of the messages kept above without adding separator rows.
		keepLines := 0
		for j := 0; j < firstDirty; j++ {
			if !hasMsgComponent(m.messages[j].Role) {
				continue
			}
			keepLines += m.msgRowCount[j]
		}
		if keepLines < len(m.lineSrcs) {
			m.lineSrcs = m.lineSrcs[:keepLines]
		}

		// Render messages. A blank separator is emitted only between two
		// messages that actually render.
		renderedAny := false
		for j := 0; j < firstDirty; j++ {
			if hasMsgComponent(m.messages[j].Role) {
				renderedAny = true
				break
			}
		}
		for i := firstDirty; i < len(m.messages); i++ {
			msg := m.messages[i]

			comp, ok := msgComponentMap[msg.Role]
			if !ok {
				// Unknown role: emit no rows at all — neither content nor the
				// blank separator. Recording zero rows keeps msgRowCount and
				// lineSrcs aligned with the grid, so later incremental renders
				// and selection mapping stay correct.
				m.msgRowCount[i] = 0
				m.msgDirty[i] = false
				continue
			}

			// Blank line between messages
			if renderedAny {
				g.AppendChunk(CellChunk{Text: "", Style: DefaultStyle})
			}
			renderedAny = true
			chunks := comp.Render(msg, false)

			// Inject TODO into the last assistant message (between reasoning and tool calls)
			// Always renders when message has TodoSnapshot — todoDirty only controls content refresh.
			if i == len(m.messages)-1 && msg.Role == "assistant" && len(msg.ToolCalls) > 0 &&
				len(msg.TodoSnapshot) > 0 {
				items := msg.TodoSnapshot
				if len(items) > 0 {
					// Find the "→ Calling tools:" index
					toolCallIdx := -1
					for ci, ch := range chunks {
						if strings.Contains(ch.Text, "→ Calling tools:") {
							toolCallIdx = ci
							break
						}
					}
					if toolCallIdx > 0 {
						// Build TODO chunks (rebuild only when dirty, otherwise reuse)
						done := 0
						for _, item := range items {
							if item.Status == "completed" || item.Status == "cancelled" {
								done++
							}
						}
						total := len(items)
						todoChunks := []CellChunk{
							{Text: "", Style: DefaultStyle},
							{Text: fmt.Sprintf("  Todo (%d/%d)", done, total), Style: HeadingStyle},
						}
						for _, item := range items {
							marker := "[ ]"
							style := DefaultStyle
							switch item.Status {
							case "in_progress":
								marker = "[>]"
								style = DimStyle
							case "completed":
								marker = "[x]"
								style = DimStyle
							case "cancelled":
								marker = "[~]"
								style = DimStyle
							}
							line := "    " + marker + " " + item.Content
							todoChunks = append(todoChunks, CellChunk{Text: line, Style: style})
						}
						// Blank line separator before → Calling tools:
						todoChunks = append(todoChunks, CellChunk{Text: "", Style: DefaultStyle})
						m.todoRowCount = len(todoChunks) + 1

						// Insert todoChunks before toolCallIdx
						chunks = append(chunks[:toolCallIdx], append(todoChunks, chunks[toolCallIdx:]...)...)
					}
				}
			}

			startRow := g.RowCount()
			if msg.Banner != nil {
				// Banner rows are pre-laid-out: each line occupies exactly one
				// grid row so the ASCII art and the column alignment survive.
				for _, line := range renderWelcomeLines(*msg.Banner, g.width) {
					g.AppendInline(line)
				}
			} else {
				for ci := 0; ci < len(chunks); ci++ {
					chunk := chunks[ci]
					if strings.HasPrefix(chunk.Text, "[") && ci+1 < len(chunks) &&
						strings.HasPrefix(chunks[ci+1].Text, " ") {
						g.AppendInline([]CellChunk{chunk, chunks[ci+1]})
						ci++
						continue
					}
					if strings.ContainsAny(chunk.Text, "│─") {
						g.AppendChunk(chunk)
						continue
					}
					wrapped := wordWrap(chunk.Text, g.width, chunk.Style)
					for _, wc := range wrapped {
						g.AppendChunk(wc)
					}
				}
			}
			endRow := g.RowCount()
			m.msgRowCount[i] = endRow - startRow

			// Remove stale buttons for this message (re-registered below)
			var kept []Button
			for _, b := range m.activeButtons {
				if b.MsgIdx != i {
					kept = append(kept, b)
				}
			}
			m.activeButtons = kept

			// Fold button
			if msg.Role == "assistant" && msg.ReasoningContent != "" {
				m.activeButtons = append(m.activeButtons, Button{
					MsgIdx: i, Line: startRow, Col: 0, Width: 3, Label: "fold",
					Action: func() {
						m.messages[i].ReasoningFolded = !m.messages[i].ReasoningFolded
						m.MarkMsgDirty(i)
					},
				})
			}

			// Build lineSrcs
			for r := startRow; r < endRow; r++ {
				text := g.RowText(r)
				field := "content"
				offset := 4
				switch msg.Role {
				case "user":
					field = "user"
					offset = 2
				case "system":
					field = "system"
					offset = 4
					if msg.Banner != nil {
						// Banner rows are indented by two columns, not four.
						offset = 2
					}
				case "assistant":
					if text == "Response:" {
						field = "label"
						offset = 0
					} else if msg.ReasoningContent != "" && r-startRow == 0 {
						field = "reasoning"
					}
				}
				m.lineSrcs = append(m.lineSrcs, lineSrc{
					MsgIdx: i, SourceField: field, Text: text, ContentOffset: offset,
				})
			}

			// [Copy] button
			if msg.Role == "assistant" && !msg.Streaming && (len(msg.Blocks) > 0 || msg.Content != "") {
				btnRow := g.RowCount()
				btnLine, col, width := ButtonComponent{}.Render("Copy", 4, false)
				plainBtn := stripANSI(btnLine)
				g.AppendChunk(CellChunk{Text: plainBtn, Style: DimStyle})
				m.lineSrcs = append(m.lineSrcs, lineSrc{MsgIdx: i, SourceField: "button", Text: ""})
				msgContent := msg.Content
				m.activeButtons = append(m.activeButtons, Button{
					MsgIdx: i, Line: btnRow, Col: col, Width: width, Label: "Copy",
					Action: func() {
						copyToClipboard(msgContent)
						m.ShowStatus("✓ Copied")
					},
				})
			}
			// Include [Copy] button in msgRowCount for correct dirtyStart
			m.msgRowCount[i] = g.RowCount() - startRow

			m.msgDirty[i] = false
		}
	}

	// Apply character-level selection highlighting
	if m.charSelStart.Offset >= 0 {
		sr, sc := m.charSelStartLine, m.charSelStartCol
		er, ec := m.charSelEndLine, m.charSelEndCol
		// Normalize: low row/col first
		if er < sr || (er == sr && ec < sc) {
			sr, sc, er, ec = er, ec, sr, sc
		}
		g.Fill(sr, sc, er, ec, SelectionStyle)
	}

	// Render grid to viewport
	rendered := g.Render()
	wasAtBottom := m.vp.AtBottom()
	m.vp.SetContent(rendered)
	// Auto-scroll: user was at bottom before new content
	if wasAtBottom {
		m.vp.GotoBottom()
	}
	b.WriteString(m.vp.View())
	b.WriteString("\n")

	// Signal TODO render ack (outside the TODO injection block, so it fires
	// even when TodoSnapshot isn't set yet — prevents agent deadlock)
	if m.todoDirty && m.renderAckCh != nil {
		m.renderAckCh <- struct{}{}
		m.renderAckCh = nil
	}
	m.todoDirty = false

	// Auto-show permission dialog even without a keypress
	m.checkPermissionDialog()

	// Input area — dialog, provider, command palette are mutually exclusive
	if m.dialogMode {
		b.WriteString(renderHeading(m.dialogMsg, m.width))
		b.WriteString("\n")
		for i, item := range m.dialogItems {
			// The always-allow answer names the file it writes; that label is
			// wider than 40 columns before its `[3] ` marker is added, so it
			// wraps under the marker instead of running off the screen.
			marker := fmt.Sprintf("[%d] ", i+1)
			prefix := "  "
			if i == m.dialogSel {
				prefix = "> "
			}
			markerIndent := len(prefix) + len(marker) // ASCII, so cells == bytes
			restIndent := len(prefix)                 // a wrapped answer lines up under its marker
			for j, line := range wrapInputText(item, m.width, markerIndent, restIndent) {
				if j == 0 {
					b.WriteString(prefix + marker + line)
				} else {
					b.WriteString(strings.Repeat(" ", restIndent) + line)
				}
				b.WriteString("\n")
			}
		}
		b.WriteString(renderHintRow(dialogHintAlternatives, m.width))
	} else if m.selectingProvider {
		b.WriteString(renderHeading("Select provider:", m.width))
		b.WriteString("\n")
		for i, rec := range m.provReg.List() {
			// The picker's label is built from the user's own provider names, so it
			// can be any length; it is budgeted like the dialog's heading, and the
			// selected row's header style is budgeted with it. The marker stays
			// outside the style so it keeps its column on every row, which is also
			// what lets a wrapped label line up under itself.
			marker := "  "
			indent := len(marker)
			if i == m.providerCursor {
				marker = "> "
				indent += headerStyle.GetHorizontalFrameSize()
			}
			label := fmt.Sprintf("%s (%s)", rec.Name, rec.Provider.Name())
			for j, line := range wrapInputText(label, m.width, indent, indent) {
				if i == m.providerCursor {
					line = headerStyle.Render(line)
				}
				// The marker belongs to the row, not to each of its lines: a wrapped
				// label continues under its own text.
				if j == 0 {
					b.WriteString(marker + line)
				} else {
					b.WriteString(strings.Repeat(" ", len(marker)) + line)
				}
				b.WriteString("\n")
			}
		}
		b.WriteString(renderHintRow(providerHintAlternatives, m.width))
	} else if m.cmdPalette {
		cmds := m.filteredCmds()
		b.WriteString(renderHeading("Commands:", m.width))
		b.WriteString("\n")
		for i, c := range cmds {
			marker, name := "  ", c.Name
			if i == m.cmdPaletteSel {
				marker, name = "> ", headerStyle.Render(c.Name)
			}
			head := marker + name
			if c.Desc == "" {
				b.WriteString(head)
				b.WriteString("\n")
				continue
			}
			// The description sits beside the name when it fits there in full, and
			// takes lines of its own under the row when it does not, where it gets the
			// whole width. Splitting it into the narrow column beside the name leaves
			// fragments like `LSP` / `diagnostics`, which is not a description
			// (issue #221).
			column := lipgloss.Width(head) + len(paletteGap)
			if lines := wrapInputText(c.Desc, m.width, column, column); len(lines) == 1 {
				b.WriteString(head + paletteGap + dimStyle.Render(lines[0]))
				b.WriteString("\n")
				continue
			}
			b.WriteString(head)
			b.WriteString("\n")
			column = len(marker) + len(paletteGap)
			for _, line := range wrapInputText(c.Desc, m.width, column, column) {
				b.WriteString(strings.Repeat(" ", column) + dimStyle.Render(line))
				b.WriteString("\n")
			}
		}
		b.WriteString(renderHintRow(paletteHintAlternatives, m.width))
	} else {
		b.WriteString(m.input.View())
	}

	// Status bar (bottom)
	b.WriteString("\n")
	b.WriteString(m.renderStatusBar())

	return b.String()
}

// renderStatusBar builds the bottom status line.
func (m *TuiModel) renderStatusBar() string {
	modeIcon := "⚡"
	spinnerStr := ""
	if m.status == StatusStreaming && m.spinner.Spinner.Frames != nil {
		spinnerStr = " " + m.spinner.View()
	}

	// Processing indicator fallback
	procStr := ""
	if m.status == StatusStreaming {
		if spinnerStr == "" {
			procStr = " ●"
		}
	}

	// Session duration
	dur := time.Since(m.sessionStart)
	durStr := formatDuration(dur)

	// Model name
	modelName := m.modeName
	if m.registry != nil {
		modelName = m.registry.CurrentName()
	}

	// Provider name
	provName := "unknown"
	if m.provReg != nil {
		provName = m.provReg.Current().Name()
	}

	// History indicator
	histStr := ""
	if m.historyPos >= 0 {
		histStr = fmt.Sprintf("  hist %d/%d", m.historyPos+1, len(m.inputHistory))
	}

	// Build status bar
	diagStr := ""
	if m.diagTotal > 0 {
		diagStr = fmt.Sprintf("  errors: %d", m.diagTotal)
	}

	statusMsg := m.statusMsg
	if statusMsg != "" {
		statusMsg = "  │ " + statusMsg
	}

	// Pair the counter with a limit only when one is configured, and only with
	// the session limit: the counter is a session total, so showing a per-run
	// limit beside it would put two different scopes in one fraction. With no
	// budget configured the text is exactly what it has always been.
	tokensStr := fmt.Sprintf("tokens: %d", m.sessionTokens)
	if m.config != nil {
		if _, perSession := m.config.TokenBudgets(); perSession > 0 {
			tokensStr = fmt.Sprintf("tokens: %d/%d", m.sessionTokens, perSession)
		}
	}
	// Cache reads sit beside the total they are part of, and only when a provider
	// reported one: a route without caching shows the bar it always showed
	// (roadmap A4). The number is the token saving, not a second total.
	if m.sessionCached > 0 {
		tokensStr += fmt.Sprintf(" (%d cached)", m.sessionCached)
	}

	// Money appears only when cost accounting is in play — a rate the user
	// declared, or a route that reported a charge. A session with neither is not
	// doing cost accounting at all, and announcing "unknown" on every run would
	// spend the bar's width on a statement nobody asked for; silence there is not
	// the same as zero. Once it is in play, the calls nobody could price are shown
	// beside the total, because "0.40 USD" and "0.40 USD, and three calls nobody
	// priced" are different claims and the second is the true one.
	costInPlay := len(m.sessionCost) > 0
	if !costInPlay && m.config != nil && m.config.Pricing != nil && len(m.config.Pricing.Prices) > 0 {
		costInPlay = true
	}
	costStr := ""
	if costInPlay {
		if len(m.sessionCost) == 0 {
			costStr = fmt.Sprintf("  cost: unknown (%d unpriced)", m.unpricedCalls)
		} else {
			costStr = "  cost: " + m.sessionCost.String()
			if m.unpricedCalls > 0 {
				costStr += fmt.Sprintf(" (+%d unpriced)", m.unpricedCalls)
			}
		}
	}

	// Goal progress is the number a user watching an unattended session needs,
	// and a phase suffix is what makes a spent goal readable without typing
	// /goal. The round count alone would leave a blocked goal looking busy.
	goalStr := ""
	if m.goals != nil {
		if g := m.goals.Snapshot(); g != nil {
			goalStr = fmt.Sprintf("  goal %d/%d", g.RoundsStarted, g.MaxGoalRounds)
			switch g.Phase {
			case goal.PhaseBlocked:
				goalStr += " blocked"
			case goal.PhaseComplete:
				goalStr += " done"
			}
		}
	}

	// The bar is a list of segments in priority order — an earlier one survives a
	// narrow terminal — plus the status message, which is budgeted first because it
	// is the part that must be readable: `Press Ctrl+C again to quit` is the only
	// signal that a first Ctrl+C was taken as a quit rather than as an interrupt,
	// and a half sentence there is not cosmetic (issue #196).
	segments := []string{
		fmt.Sprintf("%s %s%s%s", modeIcon, modelName, spinnerStr, procStr),
		fmt.Sprintf("  ■ %s", provName),
		"  " + tokensStr,
		goalStr,
		costStr,
		fmt.Sprintf("  tools: %d", m.sessionToolCalls),
		fmt.Sprintf("  msgs: %d", len(m.messages)),
		diagStr,
		fmt.Sprintf("  session: %s", durStr),
		histStr,
	}

	return statusBarStyle.Render(fitStatusBar(segments, statusMsg, m.width))
}

// fitStatusBar joins the segments that fit beside a status message, measuring in
// cells rather than characters: the bar carries wide glyphs (⚡, ■), and the
// terminal clips by cells.
//
// Two rules, both from issue #196 and both asserted:
//
//   - The message is budgeted first and kept whole. Everything else is added left
//     to right while it fits, so the segments nearest the message are the ones a
//     narrow terminal costs the user.
//   - A segment that does not fit is left out *whole*. A segment cut in half can be
//     misread — `0.95 credits` rendering as `0.95 credit`, or a fraction losing its
//     limit — and an absent segment cannot. When nothing can make the message fit,
//     it is clipped with a visible ellipsis rather than silently truncated.
//
// A width of zero means the terminal size is not known yet; the bar is then built
// in full, which is what it did before this existed.
func fitStatusBar(segments []string, message string, width int) string {
	if width <= 0 {
		return strings.Join(segments, "") + message
	}
	if lipgloss.Width(message) > width {
		return clipStatusText(message, width)
	}

	used := lipgloss.Width(message)
	kept := make([]string, 0, len(segments))
	for _, segment := range segments {
		w := lipgloss.Width(segment)
		if used+w > width {
			break
		}
		kept = append(kept, segment)
		used += w
	}
	return strings.Join(kept, "") + message
}

// clipStatusText cuts text to width cells and marks the cut. The marker matters:
// without it a clipped message reads as a shorter one.
func clipStatusText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	const marker = "…"
	limit := width - lipgloss.Width(marker)
	if limit <= 0 {
		return marker
	}
	var b strings.Builder
	used := 0
	for _, r := range text {
		w := runewidth.RuneWidth(r)
		if used+w > limit {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String() + marker
}

// The key hints drawn under the input area. Each is a list of alternatives rather
// than one finished string: at a narrow width a hint row has to be laid out again,
// not handed to a terminal that would cut it mid-alternative (issue #218).
var (
	dialogHintAlternatives   = []string{"↑↓ navigate", "Enter select", "1-9 shortcut", "Esc cancel"}
	providerHintAlternatives = []string{"↑↓ navigate", "Enter select", "Esc cancel"}
	paletteHintAlternatives  = []string{"↑↓ navigate", "Enter execute", "Esc cancel"}
)

// hintSeparator joins the alternatives of a key-hint row. hintTrailer is the same
// separator without its trailing space: it marks the end of a wrapped line, so the
// lines still read as one list and no cell is spent on the space.
const (
	hintSeparator = " · "
	hintTrailer   = " ·"
)

// paletteGap separates a command's name from its description.
const paletteGap = "  "

// renderHeading lays an input-area heading out for a terminal width cells wide and
// styles it, with the style's own horizontal padding reserved.
//
// A heading is drawn below the cell grid like the rest of the input area, so nothing
// else trims it. The permission title — `🔒 Write to <path>?` — is the long one
// (issue #218), but `Select provider:` is 16 cells before its padding, which is
// already wider than a narrow terminal (issue #221).
func renderHeading(text string, width int) string {
	pad := headerStyle.GetHorizontalFrameSize()
	lines := wrapInputText(text, width, pad, pad)
	for i, line := range lines {
		lines[i] = headerStyle.Render(line)
	}
	return strings.Join(lines, "\n")
}

// renderHintRow lays a key-hint row out for a terminal width cells wide and styles
// it. A non-positive width means the terminal size is not known yet.
func renderHintRow(alternatives []string, width int) string {
	lines := fitHintRow(alternatives, width)
	for i, line := range lines {
		lines[i] = dimStyle.Render(line)
	}
	return strings.Join(lines, "\n")
}

// fitHintRow packs the alternatives of a key-hint row into lines that fit a
// terminal width cells wide, measuring in cells rather than characters: the row
// carries wide glyphs (↑↓).
//
// Nothing is dropped to make room. `Esc cancel` is the only way out of the
// permission dialog, and a row of alternatives cut by the terminal loses exactly
// the tail a reader needs — the dialog at 40 columns was 54 cells wide and the
// user saw a sentence with no end (issue #218). Wrapping keeps every alternative
// on screen; the separator trails the wrapped line so it does not read as a second
// list. An alternative wider than the whole line is clipped with the visible
// marker rather than cut where the terminal would cut it.
//
// A width of zero means the terminal size is not known yet: the row is then built
// in full on one line, which is what it did before this existed.
func fitHintRow(alternatives []string, width int) []string {
	if width <= 0 {
		return []string{strings.Join(alternatives, hintSeparator)}
	}
	sepW := lipgloss.Width(hintSeparator)

	var rows [][]string
	row := []string{}
	used := 0
	for _, alt := range alternatives {
		w := lipgloss.Width(alt)
		if len(row) > 0 && used+sepW+w > width {
			rows = append(rows, row)
			row, used = nil, 0
		}
		if len(row) > 0 {
			used += sepW
		}
		row = append(row, alt)
		used += w
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	lines := make([]string, 0, len(rows))
	trailerW := lipgloss.Width(hintTrailer)
	for i, r := range rows {
		line := strings.Join(r, hintSeparator)
		// The separator only trails a line when there is a next one and when it
		// still fits: a line that is already full stays whole.
		if i < len(rows)-1 && lipgloss.Width(line)+trailerW <= width {
			line += hintTrailer
		}
		if lipgloss.Width(line) > width {
			line = clipStatusText(line, width)
		}
		lines = append(lines, line)
	}
	return lines
}

// wrapInputText breaks text into lines of at most width cells. The first line
// reserves firstIndent cells for the caller's own prefix (`> [3] `, or a style's
// horizontal padding); the lines after it reserve restIndent, which is normally
// smaller: a wrapped path is worth more cells than the marker above it.
//
// The input area is drawn outside the cell grid, so nothing else trims it: a line
// wider than the terminal is cut by the terminal itself, mid-word and without a
// mark. The permission dialog's heading and its always-allow answer are both wider
// than 40 columns (issue #218), so they are wrapped here, at word boundaries.
//
// A word that does not fit a line of its own is split across lines rather than
// clipped: the answer names the file a permanent permission will be written to, and
// a reader cannot consent to a path they can only half see. A width of zero means
// the terminal size is not known yet: the text is then left on one line, which is
// what it did before this existed.
func wrapInputText(text string, width, firstIndent, restIndent int) []string {
	if width <= 0 {
		return []string{text}
	}
	if text == "" {
		return []string{""}
	}

	limit := max(width-firstIndent, 1)
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && lipgloss.Width(line)+1+lipgloss.Width(word) <= limit {
			line += " " + word
			continue
		}
		if line != "" {
			lines = append(lines, line)
			line = ""
			limit = max(width-restIndent, 1)
		}
		for lipgloss.Width(word) > limit {
			head, tail := splitAtCells(word, limit)
			lines = append(lines, head)
			word = tail
			limit = max(width-restIndent, 1)
		}
		line = word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// wordBreakPoints are the characters a long token is split after when the line has
// no space to break at. A provider name or a path reads better as
// `openrouter-anthropic-` / `claude-sonnet` than as `openrouter-anthropic-claud` /
// `e-sonnet`, and this is where the split is decided.
const wordBreakPoints = "-_./:"

// splitAtCells cuts a word into the longest prefix that fits limit cells and the
// rest. The prefix ends at the last break point that fits when there is one, so a
// hyphenated name or a path is split where a reader expects it, and otherwise it is
// cut at the cell limit. At least one rune stays in the prefix, so a two-cell glyph
// at a one-cell limit cannot loop forever.
func splitAtCells(word string, limit int) (head, tail string) {
	used, cut, lastBreak := 0, 0, 0
	for i, r := range word {
		w := runewidth.RuneWidth(r)
		if i > 0 && used+w > limit {
			break
		}
		used += w
		cut = i + len(string(r))
		if i > 0 && strings.ContainsRune(wordBreakPoints, r) {
			lastBreak = cut
		}
	}
	if lastBreak > 0 {
		cut = lastBreak
	}
	return word[:cut], word[cut:]
}

// formatDuration formats a duration like "4h31m" or "32s".
func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// buildLineSrcs builds rendered lines and their source mappings from messages.
func buildLineSrcs(messages []chatMessage, vpWidth int) ([]string, []lineSrc) {
	var msgLines []string
	var srcs []lineSrc
	for i, msg := range messages {
		switch msg.Role {
		case "user":
			before := len(msgLines)
			uc := UserComponent{}
			rendered := chunksToStrings(uc.Render(msg, false))
			msgLines = append(msgLines, rendered...)
			for li := before; li < len(msgLines); li++ {
				srcs = append(srcs, lineSrc{MsgIdx: i, SourceField: "user", Text: stripANSI(rendered[li-before]), ContentOffset: 2})
			}
		case "assistant":
			before := len(msgLines)
			lines := chunksToStrings(renderAssistantMessageStatic(msg))
			msgLines = append(msgLines, lines...)
			for li := before; li < len(msgLines); li++ {
				text := stripANSI(msgLines[li])
				field := "content"
				offset := 4
				if strings.Contains(text, "Response:") {
					field = "label"
					offset = 0
				}
				srcs = append(srcs, lineSrc{MsgIdx: i, SourceField: field, Text: text, ContentOffset: offset})
			}
			if !msg.Streaming && (len(msg.Blocks) > 0 || msg.Content != "") {
				btnLine, _, _ := ButtonComponent{}.Render("Copy", 4, false)
				msgLines = append(msgLines, btnLine)
				srcs = append(srcs, lineSrc{MsgIdx: i, SourceField: "button", Text: ""})
			}
		case "system":
			before := len(msgLines)
			var chunks []CellChunk
			offset := 4
			if msg.Banner != nil {
				// Lay the banner out for this viewport, then collapse it to
				// one styled string per row (this path has no inline support).
				chunks = flattenWelcomeLines(renderWelcomeLines(*msg.Banner, vpWidth))
				offset = 2
			} else {
				chunks = SystemComponent{}.Render(msg, false)
			}
			rendered := chunksToStrings(chunks)
			msgLines = append(msgLines, rendered...)
			for li := before; li < len(msgLines); li++ {
				srcs = append(srcs, lineSrc{MsgIdx: i, SourceField: "system", Text: stripANSI(rendered[li-before]), ContentOffset: offset})
			}
		}
	}
	return msgLines, srcs
}

// chunksToStrings converts CellChunks to ANSI-styled strings (transitional).
func chunksToStrings(chunks []CellChunk) []string {
	var lines []string
	for _, c := range chunks {
		lines = append(lines, renderStyled(c.Style, c.Text))
	}
	return lines
}

// posFromCoord maps a content line and column to a character position.
// If line is beyond srcs, clamps to the last row.
func posFromCoord(line, col int, srcs []lineSrc) selPos {
	if line < 0 || len(srcs) == 0 {
		return selPos{Offset: -1}
	}
	if line >= len(srcs) {
		line = len(srcs) - 1
	}
	s := srcs[line]
	offset := col - s.ContentOffset
	if offset < 0 {
		offset = 0
	}
	// Use content/reasoning field for text extraction mapping
	field := "content"
	if s.SourceField == "reasoning" {
		field = "reasoning"
	}
	return selPos{MsgIdx: s.MsgIdx, Field: field, Offset: offset}
}

// extractSelected extracts the plain text within a character selection range.
func extractSelected(start, end selPos, messages []chatMessage) string {
	if start.Offset < 0 || end.Offset < 0 {
		return ""
	}
	// Normalize: always low → high
	if end.MsgIdx < start.MsgIdx || (end.MsgIdx == start.MsgIdx && end.Offset < start.Offset) {
		start, end = end, start
	}
	var b strings.Builder
	for i := start.MsgIdx; i <= end.MsgIdx && i < len(messages); i++ {
		msg := messages[i]

		// Include reasoning content first if available
		if msg.ReasoningContent != "" && msg.Content != "" {
			b.WriteString(msg.ReasoningContent)
			b.WriteString("\n")
		}

		text := msg.Content
		if text == "" {
			text = msg.ReasoningContent
		}
		charStart := 0
		charEnd := len(text)
		if i == start.MsgIdx {
			charStart = start.Offset
		}
		if i == end.MsgIdx {
			charEnd = end.Offset + 1
			if charEnd > len(text) {
				charEnd = len(text)
			}
		}
		if charStart < charEnd && charStart >= 0 && charEnd <= len(text) {
			b.WriteString(text[charStart:charEnd])
		}
		if i < end.MsgIdx {
			b.WriteString("\n")
		}
	}
	return b.String()
}

// renderAssistantMessageStatic renders an assistant message without model deps.
func renderAssistantMessageStatic(msg chatMessage) []CellChunk {
	ac := AssistantComponent{}
	return ac.Render(msg, false)
}

// stripANSI removes ANSI escape sequences from a string. Both CSI sequences
// (ESC [ … final byte) and OSC sequences (ESC ] … BEL or ESC \) are removed, so
// OSC 8 hyperlinks emitted by the grid never leak into copied text.
func stripANSI(s string) string {
	var b strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && !(s[i] >= 'A' && s[i] <= 'z' || s[i] >= '@' && s[i] <= '~') {
				i++
			}
			if i < len(s) {
				i++
			}
		} else if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == ']' {
			i += 2
			for i < len(s) {
				if s[i] == '\a' {
					i++
					break
				}
				if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// wrapLine splits a line into multiple lines, each no wider than maxWidth.
// Uses lipgloss.Width to properly handle ANSI codes, CJK, and emoji.
func renderBlocks(blocks []ContentBlock, sel bool) []string {
	var lines []string
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, sel)
			lines = append(lines, chunksToStrings(chunks)...)
		}
	}
	return lines
}

// renderChunks joins TextChunks into a single styled string.
// For unselected text, applies inline styles (bold, italic, code).
// For selected text, uses selectedStyle uniformly.
func renderChunks(chunks []TextChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	var b strings.Builder
	for _, c := range chunks {
		if c.Code {
			// Inline code: golden text, no background
			codeStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FDD700"))
			b.WriteString(codeStyle.Render(c.Text))
		} else if c.Bold && c.Italic {
			style := lipgloss.NewStyle().Bold(true).Italic(true)
			b.WriteString(style.Render(c.Text))
		} else if c.Bold {
			style := lipgloss.NewStyle().Bold(true)
			b.WriteString(style.Render(c.Text))
		} else if c.Italic {
			style := lipgloss.NewStyle().Italic(true)
			b.WriteString(style.Render(c.Text))
		} else if c.Link != "" {
			style := lipgloss.NewStyle().
				Foreground(currentTheme.SystemFg).
				Underline(true)
			b.WriteString(style.Render(c.Text))
		} else {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// renderTable renders a table block with aligned columns.

// renderTable renders a table block with aligned columns.
func renderTable(block ContentBlock, sel bool) []CellChunk {
	if len(block.Headers) == 0 && len(block.Rows) == 0 {
		return nil
	}

	// Collect cell texts as plain strings
	type cellInfo struct{ plainText string }
	colCount := 0
	var allRows [][]cellInfo

	if len(block.Headers) > 0 {
		var row []cellInfo
		for _, cellChunks := range block.Headers {
			plain := stripANSI(renderChunks(cellChunks))
			row = append(row, cellInfo{plainText: plain})
		}
		if len(row) > 0 {
			allRows = append(allRows, row)
			colCount = max(colCount, len(row))
		}
	}
	for _, rowCells := range block.Rows {
		var row []cellInfo
		for _, cell := range rowCells {
			plain := stripANSI(renderChunks(cell))
			row = append(row, cellInfo{plainText: plain})
		}
		if len(row) > 0 {
			allRows = append(allRows, row)
			colCount = max(colCount, len(row))
		}
	}

	colWidths := make([]int, colCount)
	for _, row := range allRows {
		for ci, cell := range row {
			w := lipgloss.Width(cell.plainText)
			if w > colWidths[ci] {
				colWidths[ci] = w
			}
		}
	}

	// Calculate available width: box-drawing chars + padding per cell
	// │  ..  │..  │ = colCount*2 + (colCount+1)*2 for bars + padding
	// Actually: outer │ (1) + indent (1) + inner │ per gap (colCount-1) + inner │ (colCount-1) + 2*colCount for padding
	// Simplified: each cell adds 2 padding + 1 right border (except last) = 2 + (colCount-1)*1
	// Outer: 2 (left border + space) + 1 (right border at end)
	// Let me use a simpler formula:
	// Total = sum(colWidths) + 3 (outer bars) + 2*colCount (padding) + colCount - 1 (inner bars)
	boxChars := 3 + 2*colCount + (colCount - 1) // │ + spaces + inner │

	// Truncate cells if total width exceeds available space
	// Approximate max line width: estimates grid width
	maxLineWidth := 120 // fallback for colWidths calculation
	if len(colWidths) > 0 {
		totalWidth := boxChars
		for _, w := range colWidths {
			totalWidth += w
		}
		if totalWidth > maxLineWidth {
			excess := totalWidth - maxLineWidth
			// Truncate the widest column(s) first
			for excess > 0 {
				maxIdx := 0
				for ci := 1; ci < colCount; ci++ {
					if colWidths[ci] > colWidths[maxIdx] {
						maxIdx = ci
					}
				}
				if colWidths[maxIdx] <= 20 {
					break // don't truncate below 20 chars
				}
				trim := excess
				if colWidths[maxIdx]-20 < trim {
					trim = colWidths[maxIdx] - 20
				}
				colWidths[maxIdx] -= trim
				excess -= trim
			}
		}
	}

	var chunks []CellChunk
	for ri, row := range allRows {
		var parts []string
		for ci := 0; ci < colCount; ci++ {
			cellText := ""
			if ci < len(row) {
				cellText = row[ci].plainText
			}
			// Truncate cell if needed
			if lipgloss.Width(cellText) > colWidths[ci] {
				// Truncate with ellipsis
				truncated := ""
				w := 0
				for _, r := range cellText {
					rw := runewidth.RuneWidth(r)
					if rw == 0 {
						rw = 1
					}
					if w+rw > colWidths[ci]-1 {
						truncated += "…"
						break
					}
					truncated += string(r)
					w += rw
				}
				cellText = truncated
			}
			padded := cellText + strings.Repeat(" ", colWidths[ci]-lipgloss.Width(cellText))
			parts = append(parts, " "+padded+" ")
		}
		line := "│" + strings.Join(parts, "│") + "│"

		rowStyle := DefaultStyle
		if sel {
			rowStyle = SelectionStyle
		} else if ri == 0 && len(block.Headers) > 0 {
			rowStyle = ResponseLabel
		}
		chunks = append(chunks, CellChunk{Text: line, Style: rowStyle})

		// Separator after header
		if ri == 0 && len(block.Headers) > 0 {
			var sepParts []string
			for ci := 0; ci < colCount; ci++ {
				sepParts = append(sepParts, strings.Repeat("─", colWidths[ci]+2))
			}
			sepLine := "├" + strings.Join(sepParts, "┼") + "┤"
			chunks = append(chunks, CellChunk{Text: sepLine, Style: DimStyle})
		}
	}
	return chunks
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
