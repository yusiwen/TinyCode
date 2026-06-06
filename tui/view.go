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
		var lines []string
		switch msg.Role {
		case "user":
			lines = append(lines, "> "+msg.Content)
		case "assistant":
			if msg.ReasoningContent != "" {
				lines = append(lines, "| "+msg.ReasoningContent)
			}
			label := "Assistant:"
			if msg.Streaming {
				label = "Assistant (streaming):"
			}
			lines = append(lines, label)
			if msg.Rendered != "" {
				lines = append(lines, msg.Rendered)
			} else if msg.Content != "" {
				lines = append(lines, msg.Content)
			}
		case "system":
			lines = append(lines, "→ "+msg.Content)
		}
		// Apply style per message
		for _, line := range lines {
			if sel {
				msgLines = append(msgLines, selectedStyle.Render(line))
			} else {
				msgLines = append(msgLines, defaultStyle.Render(line))
			}
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
