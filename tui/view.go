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

	// Message area (no header at top — moved to status bar at bottom)
	var msgLines []string
	m.activeButtons = nil
	m.lineSrcs = nil
	for i, msg := range m.messages {
		switch msg.Role {
		case "user":
			before := len(msgLines)
			uc := UserComponent{}
			rendered := uc.Render(msg, false)
			msgLines = append(msgLines, rendered...)
			for li := before; li < len(msgLines); li++ {
				m.lineSrcs = append(m.lineSrcs, lineSrc{
					MsgIdx: i, SourceField: "user",
					Text: rendered[li-before],
				})
			}
		case "assistant":
			before := len(msgLines)
			msgLines = append(msgLines, m.renderAssistantMessage(msg, false)...)
			for li := before; li < len(msgLines); li++ {
				m.lineSrcs = append(m.lineSrcs, lineSrc{
					MsgIdx: i, SourceField: "content",
					Text: msgLines[li],
				})
			}

			// Render [Copy] button
			if !msg.Streaming && (len(msg.Blocks) > 0 || msg.Content != "") {
				lineIdx := len(msgLines)
				btnLine, col, width := ButtonComponent{}.Render("Copy", 4, false)
				msgLines = append(msgLines, btnLine)
				m.lineSrcs = append(m.lineSrcs, lineSrc{
					MsgIdx: i, SourceField: "button", Text: "",
				})
				msgContent := msg.Content
				m.activeButtons = append(m.activeButtons, Button{
					MsgIdx: i, Line: lineIdx, Col: col, Width: width, Label: "Copy",
					Action: func() {
						copyToClipboard(msgContent)
						m.messages = append(m.messages, chatMessage{
							Role: "system", Content: "✓ Copied",
						})
					},
				})
			}
		case "system":
			before := len(msgLines)
			sc := SystemComponent{}
			rendered := sc.Render(msg, false)
			msgLines = append(msgLines, rendered...)
			for li := before; li < len(msgLines); li++ {
				m.lineSrcs = append(m.lineSrcs, lineSrc{
					MsgIdx: i, SourceField: "system",
					Text: rendered[li-before],
				})
			}
		}
	}

	// Wrap all lines to prevent viewport truncation
	var wrapped []string
	for _, line := range msgLines {
		wrapped = append(wrapped, wrapLine(line, m.vp.Width)...)
	}
	// Remap button lines from msgLines index to wrapped index
	for i := range m.activeButtons {
		btn := &m.activeButtons[i]
		wrappedLine := 0
		for msgLine := 0; msgLine < len(msgLines) && msgLine < btn.Line; msgLine++ {
			wrappedLine += len(wrapLine(msgLines[msgLine], m.vp.Width))
		}
		btn.Line = wrappedLine
	}

	// Apply character-level selection highlighting
	if m.charSelStart.Offset >= 0 {
		var display []string
		for i, line := range wrapped {
			var s lineSrc
			if i < len(m.lineSrcs) {
				s = m.lineSrcs[i]
			}
			inRange := s.MsgIdx >= m.charSelStart.MsgIdx && s.MsgIdx <= m.charSelEnd.MsgIdx &&
				s.SourceField != "button"
			if inRange {
				display = append(display, selectedStyle.Render(line))
			} else {
				display = append(display, line)
			}
		}
		wrapped = display
	}

	// Save scroll position before content change; scroll only if already at bottom
	wasAtBottom := m.vp.AtBottom()
	m.vp.SetContent(strings.Join(wrapped, "\n"))
	if wasAtBottom {
		m.vp.GotoBottom()
	}
	b.WriteString(m.vp.View())
	b.WriteString("\n")

	// Input area
	if m.selectingProvider {
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

	status := fmt.Sprintf("%s %s%s  ■ %s  tokens: %d  tools: %d  msgs: %d  session: %s",
		modeIcon, modelName, spinnerStr,
		provName,
		m.sessionTokens, m.sessionToolCalls, len(m.messages),
		durStr)

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
			rendered := uc.Render(msg, false)
			msgLines = append(msgLines, rendered...)
			for li := before; li < len(msgLines); li++ {
				srcs = append(srcs, lineSrc{MsgIdx: i, SourceField: "user", Text: rendered[li-before]})
			}
		case "assistant":
			before := len(msgLines)
			lines := renderAssistantMessageStatic(msg)
			msgLines = append(msgLines, lines...)
			for li := before; li < len(msgLines); li++ {
				srcs = append(srcs, lineSrc{MsgIdx: i, SourceField: "content", Text: msgLines[li]})
			}
			if !msg.Streaming && (len(msg.Blocks) > 0 || msg.Content != "") {
				btnLine, _, _ := ButtonComponent{}.Render("Copy", 4, false)
				msgLines = append(msgLines, btnLine)
				srcs = append(srcs, lineSrc{MsgIdx: i, SourceField: "button", Text: ""})
			}
		case "system":
			before := len(msgLines)
			sc := SystemComponent{}
			rendered := sc.Render(msg, false)
			msgLines = append(msgLines, rendered...)
			for li := before; li < len(msgLines); li++ {
				srcs = append(srcs, lineSrc{MsgIdx: i, SourceField: "system", Text: rendered[li-before]})
			}
		}
	}
	return msgLines, srcs
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
	textLen := len(s.Text)
	if col >= textLen {
		col = textLen - 1
	}
	if col < 0 {
		col = 0
	}
	return selPos{MsgIdx: s.MsgIdx, Offset: col}
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
func renderAssistantMessageStatic(msg chatMessage) []string {
	ac := AssistantComponent{}
	return ac.Render(msg, false)
}

// renderAssistantMessage delegates to the AssistantComponent.
// Kept for backward compatibility; tests and callers use this function.
func (m *TuiModel) renderAssistantMessage(msg chatMessage, sel bool) []string {
	answerComponent := AssistantComponent{}
	return answerComponent.Render(msg, sel)
}

// wrapLine splits a line into multiple lines, each no wider than maxWidth.
// Uses lipgloss.Width to properly handle ANSI codes, CJK, and emoji.
func wrapLine(line string, maxWidth int) []string {
	if maxWidth < 1 {
		maxWidth = 1
	}
	if lipgloss.Width(line) <= maxWidth {
		return []string{line}
	}
	var lines []string
	remaining := line
	for {
		trimmed := strings.TrimRight(remaining, "\n\r ")
		if trimmed == "" {
			break
		}
		if lipgloss.Width(trimmed) <= maxWidth {
			lines = append(lines, trimmed)
			break
		}
		// Find break point: try last space within maxWidth
		breakPos := -1
		width := 0
		runes := []rune(trimmed)
		for i, r := range runes {
			rw := lipgloss.Width(string(r))
			if width+rw > maxWidth {
				break
			}
			width += rw
			if r == ' ' || r == '\t' {
				breakPos = i + 1 // include the space
			}
		}
		if breakPos <= 0 {
			// No space found, hard break at character boundary
			w := 0
			for i, r := range runes {
				rw := lipgloss.Width(string(r))
				if w+rw > maxWidth {
					breakPos = i
					break
				}
				w += rw
				if i == len(runes)-1 {
					breakPos = len(runes)
				}
			}
		}
		if breakPos <= 0 || breakPos >= len(runes) {
			lines = append(lines, trimmed)
			break
		}
		lines = append(lines, string(runes[:breakPos]))
		remaining = string(runes[breakPos:])
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	return lines
}
func renderBlocks(blocks []ContentBlock, sel bool) []string {
	var lines []string
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			lines = append(lines, comp.Render(block, sel)...)
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
				Foreground(colorCyan).
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
func renderTable(block ContentBlock, sel bool) []string {
	if len(block.Headers) == 0 && len(block.Rows) == 0 {
		return nil
	}

	type cellInfo struct{ text string }
	colCount := 0
	var allRows [][]cellInfo

	if len(block.Headers) > 0 {
		var row []cellInfo
		for _, cellChunks := range block.Headers {
			text := renderChunks(cellChunks)
			if text != "" {
				row = append(row, cellInfo{text: text})
			}
		}
		if len(row) > 0 {
			allRows = append(allRows, row)
			colCount = max(colCount, len(row))
		}
	}
	for _, rowCells := range block.Rows {
		var row []cellInfo
		for _, cell := range rowCells {
			text := renderChunks(cell)
			if text != "" {
				row = append(row, cellInfo{text: text})
			}
		}
		if len(row) > 0 {
			allRows = append(allRows, row)
			colCount = max(colCount, len(row))
		}
	}

	colWidths := make([]int, colCount)
	for _, row := range allRows {
		for ci, cell := range row {
			w := lipgloss.Width(cell.text)
			if w > colWidths[ci] {
				colWidths[ci] = w
			}
		}
	}

	var lines []string
	sepStyle := dimStyle

	for ri, row := range allRows {
		var parts []string
		for ci := 0; ci < colCount; ci++ {
			var cellText string
			if ci < len(row) {
				cellText = row[ci].text
			}
			padded := cellText + strings.Repeat(" ", colWidths[ci]-lipgloss.Width(cellText))
			parts = append(parts, " "+padded+" ")
		}
		line := "│" + strings.Join(parts, "│") + "│"
		if ri == 0 && len(block.Headers) > 0 {
			if sel {
				lines = append(lines, selectedStyle.Render(line))
			} else {
				lines = append(lines, assistantLabelStyle.Render(line))
			}
			// Separator
			var sepParts []string
			for ci := 0; ci < colCount; ci++ {
				sepParts = append(sepParts, strings.Repeat("─", colWidths[ci]+2))
			}
			lines = append(lines, sepStyle.Render("├"+strings.Join(sepParts, "┼")+"┤"))
		} else {
			if sel {
				lines = append(lines, selectedStyle.Render(line))
			} else {
				lines = append(lines, line)
			}
		}
	}
	return lines
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
