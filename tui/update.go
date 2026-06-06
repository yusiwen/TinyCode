package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/yusiwen/tinycode/types"
)

// Update handles all events.
func (m *TuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.MouseMsg:
		if m.ready {
			// Mouse wheel → scroll viewport
			if msg.Button == tea.MouseButtonWheelUp {
				m.vp.LineUp(3)
				return m, nil
			}
			if msg.Button == tea.MouseButtonWheelDown {
				m.vp.LineDown(3)
				return m, nil
			}
			// Left button → selection
			if msg.Button == tea.MouseButtonLeft {
				contentLine := msg.Y - 1 + m.vp.YOffset
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
				case tea.MouseActionMotion:
					if m.selecting {
						m.mouseDrag = true
						m.selectEnd = idx
					}
				case tea.MouseActionRelease:
					if !m.mouseDrag {
						// Click without drag → clear selection
						m.selectStart = -1
						m.selectEnd = -1
					}
					m.selecting = false
				}
				return m, nil
			}
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
		// Provider selection dialog mode
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
							Role: "system",
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

		// Submit on Enter
		if msg.Type == tea.KeyEnter && !msg.Alt {
			if m.status != StatusStreaming && strings.TrimSpace(m.input.Value()) != "" {
				return m.submitInput()
			}
		}

		// Ctrl+J = insert newline
		if msg.Type == tea.KeyCtrlJ {
			m.input.SetValue(m.input.Value() + "\n")
			m.adjustInputHeight()
			return m, nil
		}

		// Ctrl+C: copy selection | interrupt stream | double-tap to quit
		if msg.Type == tea.KeyCtrlC {
			// ① Copy selected text if any
			if sel := m.selectedMessages(); sel != "" {
				if err := clipboard.WriteAll(sel); err == nil {
					m.messages = append(m.messages, chatMessage{
						Role: "system", Content: "✓ Copied to clipboard",
					})
				}
				// Keep selection visible, don't quit
				return m, nil
			}

			// ② Interrupt stream
			if m.status == StatusStreaming {
				m.status = StatusIdle
				m.messages = append(m.messages, chatMessage{
					Role: "system", Content: "⏹ Interrupted",
				})
				return m, nil
			}

			// ③ Double-tap to quit
			if !m.quitConfirm {
				m.quitConfirm = true
				m.messages = append(m.messages, chatMessage{
					Role: "system", Content: "Press Ctrl+C again to quit",
				})
				return m, nil
			}
			return m, tea.Quit
		}

		// Pass all other keys to textarea
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
		m.messages = append(m.messages, chatMessage{
			Role:    "user",
			Content: msg.Text,
		})
		cur := chatMessage{
			Role:      "assistant",
			Streaming: true,
		}
		m.messages = append(m.messages, cur)
		m.curAssistant = &m.messages[len(m.messages)-1]
		m.status = StatusStreaming
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
		if m.ready {
			m.vp.GotoBottom()
		}
		return m, m.waitForStream()

	case StreamDone:
		m.status = StatusIdle
		if msg.Error != nil {
			m.curAssistant.Content = fmt.Sprintf("Error: %v", msg.Error)
			m.curAssistant.Streaming = false
		} else {
			m.curAssistant.Streaming = false
			if msg.Content != "" {
				rendered, err := glamour.Render(msg.Content, m.config.GlamourStyle)
				if err == nil {
					m.curAssistant.Rendered = rendered
				} else {
					m.curAssistant.Rendered = msg.Content
				}
			}
		}
		m.curAssistant = nil
		if m.ready {
			m.vp.GotoBottom()
		}
		return m, nil

	case modeSwitchMsg:
		m.registry.Switch()
		m.agent.Config = m.registry.Current()
		m.modeName = m.registry.CurrentName()
		m.messages = append(m.messages, chatMessage{
			Role: "system", Content: fmt.Sprintf("Switched to %s mode", m.modeName),
		})
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

// messageAtLine estimates which message index corresponds to a given content line number.
func (m *TuiModel) messageAtLine(contentLine int) int {
	if contentLine < 0 || len(m.messages) == 0 {
		return 0
	}
	// Estimate terminal width for wrapping
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
				n += estimateLines(msg.ReasoningContent, termW)
			}
			n += 1 // label
			if msg.Rendered != "" {
				trimmed := strings.TrimRight(msg.Rendered, "\n")
				for _, part := range strings.Split(trimmed, "\n") {
					n += estimateLines(part, termW)
				}
			} else if msg.Content != "" {
				n += estimateLines(msg.Content, termW)
			}
		}
		if contentLine < line+n {
			return i
		}
		line += n
	}
	return -1
}

// estimateLines estimates how many terminal lines a string occupies given the terminal width.
func estimateLines(s string, w int) int {
	if len(s) == 0 {
		return 1
	}
	lines := 1
	pos := 0
	for _, r := range s {
		if r == '\n' {
			lines++
			pos = 0
		} else {
			pos++
			if pos >= w {
				lines++
				pos = 0
			}
		}
	}
	return lines
}

// isSelected returns whether message at index i is currently highlighted.
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

// selectedMessages returns the text of all selected assistant messages,
// including reasoning content.
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
		if m.messages[i].Role != "assistant" {
			continue
		}
		if m.messages[i].ReasoningContent != "" {
			b.WriteString(m.messages[i].ReasoningContent)
			b.WriteString("\n\n")
		}
		if m.messages[i].Content != "" {
			b.WriteString(m.messages[i].Content)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String())
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
	case "/model":
		m.selectingProvider = true
		m.providerCursor = 0
	case "/plan":
		if err := m.registry.Set("plan"); err != nil {
			m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Error: %v", err)})
			return m, nil
		}
		m.agent.Config = m.registry.Current()
		m.modeName = "plan"
		m.messages = append(m.messages, chatMessage{Role: "system", Content: "Switched to plan mode"})
	case "/build":
		if err := m.registry.Set("build"); err != nil {
			m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Error: %v", err)})
			return m, nil
		}
		m.agent.Config = m.registry.Current()
		m.modeName = "build"
		m.messages = append(m.messages, chatMessage{Role: "system", Content: "Switched to build mode"})
	default:
		m.messages = append(m.messages, chatMessage{Role: "system", Content: fmt.Sprintf("Unknown command: %s", cmd)})
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
