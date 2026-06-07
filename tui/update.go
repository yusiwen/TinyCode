package tui

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
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
				tlog.Debug("mouse.select", "press",
					"y", msg.Y, "contentLine", contentLine,
					"yOffset", m.vp.YOffset, "msgIdx", idx,
					"msgRole", m.messages[idx].Role)
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
					m.selectStart = -1
					m.selectEnd = -1
					tlog.Debug("mouse.select", "click",
						"y", msg.Y, "contentLine", contentLine,
						"msgIdx", idx, "msgRole", m.messages[idx].Role,
						"action", "cleared")
				} else {
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
						m.messages = append(m.messages, chatMessage{
							Role:    "system",
							Content: fmt.Sprintf("Switched to %s", m.provReg.CurrentName()),
						})
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

		// Tab on empty input → mode switch
		if msg.Type == tea.KeyTab && m.input.Value() == "" {
			return m, func() tea.Msg { return modeSwitchMsg{} }
		}

		// Enter → submit
		if msg.Type == tea.KeyEnter && !msg.Alt {
			if m.status != StatusStreaming && strings.TrimSpace(m.input.Value()) != "" {
				return m.submitInput()
			}
		}

		// Ctrl+J → newline
		if msg.Type == tea.KeyCtrlJ {
			m.input.SetValue(m.input.Value() + "\n")
			m.adjustInputHeight()
			return m, nil
		}

		// Ctrl+C: copy | interrupt | double-tap quit
		if msg.Type == tea.KeyCtrlC {
			if sel := m.selectedMessages(); sel != "" {
				copyToClipboard(sel)
				m.selectStart = -1
				m.selectEnd = -1
				m.messages = append(m.messages, chatMessage{
					Role: "system", Content: "✓ Copied to clipboard",
				})
				m.autoScroll()
				return m, nil
			}
			if m.status == StatusStreaming {
				m.status = StatusIdle
				m.messages = append(m.messages, chatMessage{
					Role: "system", Content: "⏹ Interrupted",
				})
				m.autoScroll()
				return m, nil
				}
				if !m.quitConfirm {
				m.quitConfirm = true
				m.messages = append(m.messages, chatMessage{
					Role: "system", Content: "Press Ctrl+C again to quit",
				})
				m.autoScroll()
				return m, nil
			}
			return m, tea.Quit
		}

		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.adjustInputHeight()
		return m, cmd

	case spinner.TickMsg:
		if m.status == StatusStreaming {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	case ChatMsg:
		m.messages = append(m.messages, chatMessage{Role: "user", Content: msg.Text})
		cur := chatMessage{Role: "assistant", Streaming: true}
		m.messages = append(m.messages, cur)
		m.curAssistant = &m.messages[len(m.messages)-1]
		m.status = StatusStreaming
		m.streamDoneNotified = false
		go m.runAgent(msg.Text)
		return m, m.waitForStream()

	case StreamMsg:
		if m.curAssistant == nil {
			return m, m.waitForStream()
		}
		if msg.ReasoningDelta != "" {
			m.curAssistant.ReasoningContent += msg.ReasoningDelta
		}
		if msg.TextDelta != "" {
			m.curAssistant.Content += msg.TextDelta
		}
		m.autoScroll()
		return m, m.waitForStream()

	case StreamDone:
		m.status = StatusIdle
		if msg.Error != nil {
			m.curAssistant.Content = fmt.Sprintf("Error: %v", msg.Error)
			m.curAssistant.Streaming = false
		} else {
			m.curAssistant.Streaming = false
			if msg.Content != "" {
				m.curAssistant.Blocks = parseMarkdown(msg.Content)
			}
		}
		m.curAssistant = nil
		m.streamDoneNotified = false
		m.autoScroll()
		return m, nil

	case modeSwitchMsg:
		m.registry.Switch()
		m.agent.Config = m.registry.Current()
		m.modeName = m.registry.CurrentName()
		m.messages = append(m.messages, chatMessage{
			Role: "system", Content: fmt.Sprintf("Switched to %s mode", m.modeName),
		})
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
			if msg.Content != "" {
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
// autoScroll scrolls to bottom only if user is already at the bottom.
func (m *TuiModel) autoScroll() {
	if m.ready && m.vp.AtBottom() {
		m.vp.GotoBottom()
	}
}

func (m *TuiModel) submitInput() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	m.lastInput = text
	m.input.Reset()
	if strings.HasPrefix(text, "/") {
		return m.handleCommand(text)
	}
	return m, func() tea.Msg { return ChatMsg{Text: text} }
}

func (m *TuiModel) handleCommand(cmd string) (tea.Model, tea.Cmd) {
	switch cmd {
	case "/exit", "/quit":
		return m, tea.Quit
	case "/verbose":
		m.agent.Verbose = !m.agent.Verbose
		s := "off"
		if m.agent.Verbose {
			s = "on"
		}
		m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Verbose mode %s", s)})
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
	case "/plan":
		if err := m.registry.Set("plan"); err != nil {
			m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Error: %v", err)})
			m.autoScroll()
			return m, nil
		}
		m.agent.Config = m.registry.Current()
		m.modeName = "plan"
		m.messages = append(m.messages, chatMessage{Role: "system", Content: "Switched to plan mode"})
		m.autoScroll()
	case "/build":
		if err := m.registry.Set("build"); err != nil {
			m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Error: %v", err)})
			m.autoScroll()
			return m, nil
		}
		m.agent.Config = m.registry.Current()
		m.modeName = "build"
		m.messages = append(m.messages, chatMessage{Role: "system", Content: "Switched to build mode"})
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

func (m *TuiModel) runAgent(prompt string) {
	ctx := context.Background()
	m.agent.StreamCallbacks = &types.StreamCallbacks{
		OnReasoningDelta: func(text string) {
			m.streamCh <- StreamMsg{ReasoningDelta: text}
		},
		OnTextDelta: func(text string) {
			m.streamCh <- StreamMsg{TextDelta: text}
		},
	}
	result, err := m.agent.Run(ctx, prompt)
	m.agent.StreamCallbacks = nil
	m.streamCh <- StreamDone{
		Content: result,
		Error:   err,
	}
}
