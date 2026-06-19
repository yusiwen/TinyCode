package tui

import (
	"fmt"
	"time"

	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View renders the TUI layout.
func (m *TuiModel) View() string {
	if !m.ready {
		return "Loading..."
	}

	var b strings.Builder

	// Message area — built with CellGrid
	m.activeButtons = nil
	m.ensureMsgTracking()

	// Find first dirty message (recalc start row from previous messages' rowCount)
	firstDirty := -1
	for i := range m.messages {
		if m.msgDirty[i] {
			firstDirty = i
			break
		}
	}

	if m.grid == nil || m.grid.width != m.vp.Width {
		// Resize: full rebuild
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

		// Compute the grid row where firstDirty starts
		dirtyStart := 0
		for j := 0; j < firstDirty; j++ {
			dirtyStart += m.msgRowCount[j]
			if j > 0 {
				dirtyStart++ // inter-message blank line (message 0 has no preceding blank)
			}
		}

		// Adjust for TODO section at top (rendered before messages)
		if m.todoRowCount > 0 && firstDirty == 0 {
			dirtyStart = m.todoRowCount
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

		// Truncate lineSrcs back
		keepLines := 0
		for j := 0; j < firstDirty; j++ {
			keepLines += m.msgRowCount[j]
			if j > 0 {
				keepLines++ // inter-message blank line
			}
		}
		if keepLines < len(m.lineSrcs) {
			m.lineSrcs = m.lineSrcs[:keepLines]
		}

		// Render messages
		for i := firstDirty; i < len(m.messages); i++ {
			msg := m.messages[i]

			// Blank line between messages
			if i > 0 {
				g.AppendChunk(CellChunk{Text: "", Style: DefaultStyle})
			}

			comp, ok := msgComponentMap[msg.Role]
			if !ok {
				m.msgRowCount[i] = 0
				m.msgDirty[i] = false
				continue
			}
			chunks := comp.Render(msg, false)

			// Inject TODO into the last assistant message (between reasoning and tool calls)
			if i == len(m.messages)-1 && msg.Role == "assistant" && len(msg.ToolCalls) > 0 &&
				m.todoStore != nil && m.todoDirty {
				items := m.todoStore.Read()
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
						// Insert TODO chunks before tool calls
						summary := m.todoStore.Summary()
						done := summary.Completed + summary.Cancelled
						total := summary.Total
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
						m.todoRowCount = len(todoChunks) + 1 // +1 for blank line before tool calls
						m.todoDirty = false

						// Signal render ack: agent goroutine is waiting for TODO
						// to be visible in the CellGrid before proceeding.
						if m.renderAckCh != nil {
							m.renderAckCh <- struct{}{}
							m.renderAckCh = nil
						}

						// Insert todoChunks before toolCallIdx
						chunks = append(chunks[:toolCallIdx], append(todoChunks, chunks[toolCallIdx:]...)...)
					}
				}
			}

			startRow := g.RowCount()
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
			endRow := g.RowCount()
			m.msgRowCount[i] = endRow - startRow

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

	// Input area — dialog, provider, command palette are mutually exclusive
	if m.dialogMode {
		b.WriteString(headerStyle.Render(m.dialogMsg))
		b.WriteString("\n")
		for i, item := range m.dialogItems {
			label := fmt.Sprintf("  [%d] %s", i+1, item)
			if i == m.dialogSel {
				b.WriteString(headerStyle.Render("> " + label))
			} else {
				b.WriteString("  " + label)
			}
			b.WriteString("\n")
		}
		b.WriteString(dimStyle.Render("↑↓ navigate · Enter select · 1-9 shortcut · Esc cancel"))
	} else if m.selectingProvider {
		b.WriteString(headerStyle.Render("Select provider:"))
		b.WriteString("\n")
		for i, rec := range m.provReg.List() {
			label := fmt.Sprintf("%s (%s)", rec.Name, rec.Provider.Name())
			if i == m.providerCursor {
				b.WriteString(headerStyle.Render("> " + label))
			} else {
				b.WriteString("  " + label)
			}
			b.WriteString("\n")
		}
		b.WriteString(dimStyle.Render("↑↓ navigate · Enter select · Esc cancel"))
	} else if m.cmdPalette {
		cmds := m.filteredCmds()
		b.WriteString(headerStyle.Render("Commands:"))
		b.WriteString("\n")
		for i, c := range cmds {
			if i == m.cmdPaletteSel {
				b.WriteString("> ")
				b.WriteString(headerStyle.Render(c.Name))
				b.WriteString("  ")
				b.WriteString(dimStyle.Render(c.Desc))
			} else {
				b.WriteString("  ")
				b.WriteString(c.Name)
				b.WriteString("  ")
				b.WriteString(dimStyle.Render(c.Desc))
			}
			b.WriteString("\n")
		}
		b.WriteString(dimStyle.Render("↑↓ navigate · Enter execute · Esc cancel"))
	} else if m.status == StatusStreaming {
		b.WriteString(dimStyle.Render("(processing...)"))
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

	status := fmt.Sprintf("%s %s%s  ■ %s  tokens: %d  tools: %d  msgs: %d%s  session: %s%s%s",
		modeIcon, modelName, spinnerStr,
		provName,
		m.sessionTokens, m.sessionToolCalls, len(m.messages),
		diagStr,
		durStr, histStr, statusMsg)

	return statusBarStyle.Render(status)
}
// providerName returns the current provider's display name.
func (m *TuiModel) providerName() string {
	if m.provReg == nil {
		return "unknown"
	}
	return m.provReg.Current().Name()
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
			sc := SystemComponent{}
			rendered := chunksToStrings(sc.Render(msg, false))
			msgLines = append(msgLines, rendered...)
			for li := before; li < len(msgLines); li++ {
				srcs = append(srcs, lineSrc{MsgIdx: i, SourceField: "system", Text: stripANSI(rendered[li-before]), ContentOffset: 4})
			}
		}
	}
	return msgLines, srcs
}

// chunksToStrings converts CellChunks to ANSI-styled strings (transitional).
func chunksToStrings(chunks []CellChunk) []string {
	var lines []string
	for _, c := range chunks {
		ls := styleToLipgloss(c.Style)
		lines = append(lines, ls.Render(c.Text))
	}
	return lines
}

// posFromCoord maps a content line and column to a character position.
func posFromCoord(line, col int, srcs []lineSrc) selPos {
	if line < 0 || line >= len(srcs) {
		return selPos{Offset: -1}
	}
	s := srcs[line]
	if s.SourceField == "button" {
		return selPos{Offset: -1}
	}
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


// stripANSI removes ANSI escape sequences from a string.
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
		} else {
			b.WriteByte(s[i])
			i++
		}
	}
	return b.String()
}

// renderAssistantMessage delegates to the AssistantComponent.
// Kept for backward compatibility; tests and callers use this function.
func (m *TuiModel) renderAssistantMessage(msg chatMessage, sel bool) []string {
	answerComponent := AssistantComponent{}
	return chunksToStrings(answerComponent.Render(msg, sel))
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

	var chunks []CellChunk
	for ri, row := range allRows {
		var parts []string
		for ci := 0; ci < colCount; ci++ {
			cellText := ""
			if ci < len(row) {
				cellText = row[ci].plainText
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
