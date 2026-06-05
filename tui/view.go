package tui

import (
	"fmt"
	"strings"
)

// View renders the TUI layout.
func (m *TuiModel) View() string {
	if !m.ready {
		return "Loading..."
	}

	var b strings.Builder

	// Header
	header := fmt.Sprintf("⚡ %s", m.modeName)
	if m.status == StatusStreaming {
		header += fmt.Sprintf(" %s", m.spinner.View())
	}
	b.WriteString(headerStyle.Render(header))
	b.WriteString("\n")

	// Message area
	var msgLines []string
	for _, msg := range m.messages {
		switch msg.Role {
		case "user":
			msgLines = append(msgLines, userStyle.Render("> "+msg.Content))
		case "assistant":
			if msg.Rendered != "" {
				msgLines = append(msgLines, assistantLabelStyle.Render("Assistant:"))
				msgLines = append(msgLines, msg.Rendered)
			} else if msg.Content != "" {
				label := "Assistant:"
				if msg.Streaming {
					label = "Assistant (streaming):"
				}
				msgLines = append(msgLines, assistantLabelStyle.Render(label))
				msgLines = append(msgLines, msg.Content)
			}
			if msg.ReasoningContent != "" {
				msgLines = append(msgLines, thinkingStyle.Render("| "+msg.ReasoningContent))
			}
		case "system":
			msgLines = append(msgLines, dimStyle.Render("→ "+msg.Content))
		}
	}
	m.vp.SetContent(strings.Join(msgLines, "\n"))
	b.WriteString(m.vp.View())
	b.WriteString("\n")

	// Input area
	if m.selectingProvider {
		b.WriteString(headerStyle.Render("Select provider:"))
		b.WriteString("\n")
		names := []string{
			"DeepSeek (deepseek-v4-flash)",
			"Ollama (qwen3.5:2b @ 192.168.2.41)",
			"Ollama (qwen3.5:4b @ 192.168.2.41)",
		}
		for i, name := range names {
			if i == m.providerCursor {
				b.WriteString(headerStyle.Render("> " + name))
			} else {
				b.WriteString("  " + name)
			}
			b.WriteString("\n")
		}
		b.WriteString(dimStyle.Render("↑↓ navigate · Enter select · Esc cancel"))
	} else if m.status == StatusStreaming {
		b.WriteString(dimStyle.Render("(processing...)"))
	} else {
		b.WriteString(m.input.View())
	}

	return b.String()
}
