package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// MessageComponent is the interface for rendering a single chat message.
type MessageComponent interface {
	Render(msg chatMessage, sel bool) []string
}

// BlockComponent is the interface for rendering a single ContentBlock.
type BlockComponent interface {
	Render(block ContentBlock, sel bool) []string
}

// === Component Maps ===

// msgComponentMap maps message roles to their rendering components.
var msgComponentMap = map[string]MessageComponent{
	"user":      UserComponent{},
	"assistant": AssistantComponent{},
	"system":    SystemComponent{},
}

// blockComponentMap maps ContentBlock types to their rendering components.
var blockComponentMap = map[string]BlockComponent{
	"paragraph": ParagraphComponent{},
	"heading":   HeadingComponent{},
	"code":      CodeComponent{},
	"list":      ListComponent{},
	"quote":     QuoteComponent{},
	"hr":        HRComponent{},
	"table":     TableComponent{},
}

// === Message Components ===

// UserComponent renders user messages (> prefix, green, bold).
type UserComponent struct{}

func (UserComponent) Render(msg chatMessage, sel bool) []string {
	if sel {
		return []string{selectedStyle.Render("> " + msg.Content)}
	}
	return []string{userStyle.Render("> " + msg.Content)}
}

// SystemComponent renders system messages (→ prefix, dim gray).
type SystemComponent struct{}

func (SystemComponent) Render(msg chatMessage, sel bool) []string {
	if sel {
		return []string{selectedStyle.Render("→ " + msg.Content)}
	}
	return []string{dimStyle.Render("→ " + msg.Content)}
}

// AssistantComponent renders reasoning + label + answer/streaming.
type AssistantComponent struct{}

func (AssistantComponent) Render(msg chatMessage, sel bool) []string {
	var lines []string

	// Reasoning content — indented 4 spaces
	if msg.ReasoningContent != "" {
		rc := ReasoningComponent{}
		lines = append(lines, rc.Render(msg, sel)...)
	}

	// "Assistant:" label — no indent
	if !sel {
		lines = append(lines, assistantLabelStyle.Render("Assistant:"))
	} else {
		lines = append(lines, selectedStyle.Render("Assistant:"))
	}

	// Blocks (completed answer) or streaming content
	if len(msg.Blocks) > 0 {
		ac := AnswerComponent{}
		lines = append(lines, ac.Render(msg, sel)...)
	} else if msg.Content != "" {
		sc := StreamingComponent{}
		lines = append(lines, sc.Render(msg, sel)...)
	}

	return lines
}

// ReasoningComponent renders thinking/reasoning text (dark yellow, indented).
type ReasoningComponent struct{}

func (ReasoningComponent) Render(msg chatMessage, sel bool) []string {
	var lines []string
	for _, rLine := range strings.Split(msg.ReasoningContent, "\n") {
		if sel {
			lines = append(lines, selectedStyle.Render("    "+rLine))
		} else {
			lines = append(lines, thinkingStyle.Render("    "+rLine))
		}
	}
	return lines
}

// AnswerComponent renders structured Blocks (indented 4 spaces).
type AnswerComponent struct{}

func (AnswerComponent) Render(msg chatMessage, sel bool) []string {
	var lines []string
	blocksLines := renderBlocks(msg.Blocks, sel)
	for _, bl := range blocksLines {
		if sel {
			lines = append(lines, selectedStyle.Render("    "+bl))
		} else {
			lines = append(lines, "    "+bl)
		}
	}
	return lines
}

// StreamingComponent renders raw markdown text during streaming.
type StreamingComponent struct{}

func (StreamingComponent) Render(msg chatMessage, sel bool) []string {
	label := "Assistant:"
	if msg.Streaming {
		label = "Assistant (streaming):"
	}
	if sel {
		return []string{
			selectedStyle.Render(label),
			selectedStyle.Render(msg.Content),
		}
	}
	return []string{
		assistantLabelStyle.Render(label),
		msg.Content,
	}
}

// === Block Components ===

// ParagraphComponent renders a paragraph with inline styling.
type ParagraphComponent struct{}

func (ParagraphComponent) Render(block ContentBlock, sel bool) []string {
	text := renderChunks(block.Chunks)
	if text == "" {
		return nil
	}
	if sel {
		return []string{selectedStyle.Render(text)}
	}
	return []string{text}
}

// HeadingComponent renders a heading (bright white, bold, spacing).
type HeadingComponent struct{}

func (HeadingComponent) Render(block ContentBlock, sel bool) []string {
	text := renderChunks(block.Chunks)
	if text == "" {
		return nil
	}
	lines := []string{""} // blank line before
	if sel {
		lines = append(lines, selectedStyle.Render(text))
	} else {
		style := lipgloss.NewStyle().Bold(true).Foreground(colorBrightWhite)
		lines = append(lines, style.Render(text))
	}
	lines = append(lines, "") // blank line after
	return lines
}

// CodeComponent renders a code block (dim style).
type CodeComponent struct{}

func (CodeComponent) Render(block ContentBlock, sel bool) []string {
	code := block.Code
	if code == "" {
		return nil
	}
	var lines []string
	for _, codeLine := range strings.Split(code, "\n") {
		if sel {
			lines = append(lines, selectedStyle.Render("  "+codeLine))
		} else {
			lines = append(lines, dimStyle.Render("  "+codeLine))
		}
	}
	return lines
}

// ListComponent renders ordered/unordered lists.
type ListComponent struct{}

func (ListComponent) Render(block ContentBlock, sel bool) []string {
	var lines []string
	for i, item := range block.Items {
		var prefix string
		if block.Numbered {
			prefix = fmt.Sprintf("  %d. ", i+1)
		} else {
			prefix = "  • "
		}
		// Code block inside a list item
		if item.Type == "code" {
			for _, codeLine := range strings.Split(item.Code, "\n") {
				if sel {
					lines = append(lines, selectedStyle.Render(prefix+codeLine))
				} else {
					lines = append(lines, dimStyle.Render(prefix+codeLine))
				}
			}
			continue
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
	return lines
}

// QuoteComponent renders blockquote content.
type QuoteComponent struct{}

func (QuoteComponent) Render(block ContentBlock, sel bool) []string {
	var lines []string
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
	return lines
}

// HRComponent renders a horizontal rule.
type HRComponent struct{}

func (HRComponent) Render(block ContentBlock, sel bool) []string {
	rule := strings.Repeat("─", 40)
	if sel {
		return []string{selectedStyle.Render(rule)}
	}
	return []string{dimStyle.Render(rule)}
}

// TableComponent renders a table with box-drawing characters.
type TableComponent struct{}

func (TableComponent) Render(block ContentBlock, sel bool) []string {
	return renderTable(block, sel)
}
