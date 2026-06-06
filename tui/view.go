package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
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
			line := "> " + msg.Content
			if sel {
				msgLines = append(msgLines, selectedStyle.Render(line))
			} else {
				msgLines = append(msgLines, userStyle.Render(line))
			}
		case "assistant":
			lines := m.renderAssistantMessage(msg, sel)
			msgLines = append(msgLines, lines...)
		case "system":
			line := "→ " + msg.Content
			if sel {
				msgLines = append(msgLines, selectedStyle.Render(line))
			} else {
				msgLines = append(msgLines, dimStyle.Render(line))
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

// renderAssistantMessage produces rendered terminal lines from a chatMessage.
// Uses Blocks when available, falls back to Rendered/Content for legacy messages.
func (m *TuiModel) renderAssistantMessage(msg chatMessage, sel bool) []string {
	var lines []string

	// Reasoning content
	if msg.ReasoningContent != "" {
		for _, rLine := range strings.Split(msg.ReasoningContent, "\n") {
			if sel {
				lines = append(lines, selectedStyle.Render("| "+rLine))
			} else {
				lines = append(lines, thinkingStyle.Render("| "+rLine))
			}
		}
	}

	// Blocks (new pipeline)
	if len(msg.Blocks) > 0 {
		if !sel {
			lines = append(lines, assistantLabelStyle.Render("Assistant:"))
		} else {
			lines = append(lines, selectedStyle.Render("Assistant:"))
		}
		blocksLines := renderBlocks(msg.Blocks, sel)
		lines = append(lines, blocksLines...)
		return lines
	}

	// Fallback: legacy Rendered content (glamour)
	if msg.Rendered != "" {
		if sel {
			lines = append(lines, assistantLabelStyle.Render("Assistant:"))
			lines = append(lines, selectedStyle.Render(msg.Rendered))
		} else {
			lines = append(lines, assistantLabelStyle.Render("Assistant:"))
			lines = append(lines, msg.Rendered)
		}
	} else if msg.Content != "" {
		label := "Assistant:"
		if msg.Streaming {
			label = "Assistant (streaming):"
		}
		if sel {
			lines = append(lines, selectedStyle.Render(label))
			lines = append(lines, selectedStyle.Render(msg.Content))
		} else {
			lines = append(lines, assistantLabelStyle.Render(label))
			lines = append(lines, msg.Content)
		}
	}

	return lines
}

// renderBlocks converts ContentBlocks into terminal lines.
func renderBlocks(blocks []ContentBlock, sel bool) []string {
	var lines []string

	for _, block := range blocks {
		switch block.Type {
		case "paragraph":
			text := renderChunks(block.Chunks)
			if text != "" {
				lines = append(lines, text)
			}

		case "heading":
			text := renderChunks(block.Chunks)
			if text != "" {
				prefix := strings.Repeat("#", block.Level) + " "
				if sel {
					lines = append(lines, selectedStyle.Render(prefix+text))
				} else {
					// Heading: bold + larger/brighter
					style := lipgloss.NewStyle().Bold(true).Foreground(colorCyan)
					lines = append(lines, style.Render(prefix+text))
				}
			}

		case "code":
			if sel {
				for _, codeLine := range strings.Split(block.Code, "\n") {
					lines = append(lines, selectedStyle.Render("  "+codeLine))
				}
			} else {
				for _, codeLine := range strings.Split(block.Code, "\n") {
					lines = append(lines, dimStyle.Render("  "+codeLine))
				}
			}

		case "list":
			for i, item := range block.Items {
				var prefix string
				if block.Numbered {
					prefix = fmt.Sprintf("  %d. ", i+1)
				} else {
					prefix = "  • "
				}
				text := renderChunks(item.Chunks)
				if text != "" {
					if sel {
						lines = append(lines, selectedStyle.Render(prefix+text))
					} else {
						lines = append(lines, prefix+text)
					}
				}
			}

		case "quote":
			for _, item := range block.Items {
				text := renderChunks(item.Chunks)
				if text != "" {
					if sel {
						lines = append(lines, selectedStyle.Render("> "+text))
					} else {
						lines = append(lines, dimStyle.Render("> "+text))
					}
				}
			}

		case "hr":
			width := 40
			rule := strings.Repeat("─", width)
			if sel {
				lines = append(lines, selectedStyle.Render(rule))
			} else {
				lines = append(lines, dimStyle.Render(rule))
			}
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
			// Inline code: dim background, light text
			codeStyle := lipgloss.NewStyle().
				Background(lipgloss.Color("#333333")).
				Foreground(lipgloss.Color("#FF9999")).
				Padding(0, 1)
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
