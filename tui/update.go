package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/yusiwen/tinycode/types"
)

// Update handles all events.
func (m *TuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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
				// Switch to selected provider
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

		// Submit on Enter (plain, no Alt): submit if non-empty input, 
		// otherwise pass to textarea for newline insertion.
		if msg.Type == tea.KeyEnter && !msg.Alt {
			if m.status != StatusStreaming && strings.TrimSpace(m.input.Value()) != "" {
				return m.submitInput()
			}
			// streaming or empty input → let textarea handle the key
		}

		// Ctrl+J = insert newline (line feed) for terminals that can't send
		// Shift+Enter or Alt+Enter.
		if msg.Type == tea.KeyCtrlJ {
			m.input.SetValue(m.input.Value() + "\n")
			return m, nil
		}

		// Ctrl+C to interrupt or quit
		if msg.Type == tea.KeyCtrlC {
			if m.status == StatusStreaming {
				m.status = StatusIdle
				return m, nil
			}
			return m, tea.Quit
		}

		// Pass all other keys to textarea (including Shift+Enter for newline)
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
		// Add user message
		m.messages = append(m.messages, chatMessage{
			Role:    "user",
			Content: msg.Text,
		})

		// Add placeholder assistant message
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
		// Force recalc of internal scroll/view state
		m.input.SetValue(val)
		if m.ready {
			m.vp.Height = m.height - 1 - wanted
		}
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
	case "/model":
		m.selectingProvider = true
		m.providerCursor = 0
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
	m.agent.StreamCallbacks = nil // reset for non-TUI usage
	m.streamCh <- StreamDone{
		Content: result,
		Error:   err,
	}
}
