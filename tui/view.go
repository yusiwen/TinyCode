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
	for i, msg := range m.messages {
		sel := m.isSelected(i)
		switch msg.Role {
		case "user":
			if sel {
				msgLines = append(msgLines, selectedStyle.Render("> "+msg.Content))
			} else {
				msgLines = append(msgLines, userStyle.Render("> "+msg.Content))
			}
		case "assistant":
			if msg.ReasoningContent != "" {
				msgLines = append(msgLines, thinkingStyle.Render("| "+msg.ReasoningContent))
			}
			label := "Assistant:"
			if msg.Streaming {
				label = "Assistant (streaming):"
			}
			if sel {
				msgLines = append(msgLines, selectedStyle.Render(label))
			} else {
				msgLines = append(msgLines, assistantLabelStyle.Render(label))
			}
			if msg.Rendered != "" {
				if sel {
					msgLines = append(msgLines, selectedStyle.Render(msg.Rendered))
				} else {
					msgLines = append(msgLines, msg.Rendered)
				}
			} else if msg.Content != "" {
				if sel {
					msgLines = append(msgLines, selectedStyle.Render(msg.Content))
				} else {
					msgLines = append(msgLines, msg.Content)
				}
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

	return b.String()
}
