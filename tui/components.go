package tui

import (
	"fmt"
	"strings"
)

// MessageComponent is the interface for rendering a single chat message.
type MessageComponent interface {
	Render(msg chatMessage, sel bool) []CellChunk
}

// BlockComponent is the interface for rendering a single ContentBlock.
type BlockComponent interface {
	Render(block ContentBlock, sel bool) []CellChunk
}

// --- Component Maps ---

var msgComponentMap = map[string]MessageComponent{
	"user":      UserComponent{},
	"assistant": AssistantComponent{},
	"system":    SystemComponent{},
}

var blockComponentMap = map[string]BlockComponent{
	"paragraph": ParagraphComponent{},
	"heading":   HeadingComponent{},
	"code":      CodeComponent{},
	"list":      ListComponent{},
	"quote":     QuoteComponent{},
	"hr":        HRComponent{},
	"table":     TableComponent{},
}

// ===== Message Components =====

// UserComponent renders user messages (> prefix, green, bold).
type UserComponent struct{}

func (UserComponent) Render(msg chatMessage, sel bool) []CellChunk {
	style := UserStyle
	if sel {
		style = SelectionStyle
	}
	return []CellChunk{{Text: "> " + msg.Content, Style: style}}
}

// SystemComponent renders system messages (→ prefix, dim gray).
type SystemComponent struct{}

func (SystemComponent) Render(msg chatMessage, sel bool) []CellChunk {
	style := SystemStyle
	if sel {
		style = SelectionStyle
	}
	return []CellChunk{{Text: "→ " + msg.Content, Style: style}}
}

// AssistantComponent renders reasoning + label + answer/streaming.
type AssistantComponent struct{}

func (AssistantComponent) Render(msg chatMessage, sel bool) []CellChunk {
	var chunks []CellChunk

	// Reasoning content — indented 4 spaces
	if msg.ReasoningContent != "" {
		rc := ReasoningComponent{}
		chunks = append(chunks, rc.Render(msg, sel)...)
	}

	// "Response:" label — no indent, with blank line before
	chunks = append(chunks, CellChunk{Text: "", Style: DefaultStyle})
	labelStyle := ResponseLabel
	if sel {
		labelStyle = SelectionStyle
	}
	chunks = append(chunks, CellChunk{Text: "Response:", Style: labelStyle})

	// Blocks (completed answer) or streaming content
	if len(msg.Blocks) > 0 {
		ac := AnswerComponent{}
		chunks = append(chunks, ac.Render(msg, sel)...)
	} else if msg.Content != "" {
		sc := StreamingComponent{}
		chunks = append(chunks, sc.Render(msg, sel)...)
	}

	return chunks
}

// ReasoningComponent renders thinking/reasoning text (dark yellow, indented).
type ReasoningComponent struct{}

func (ReasoningComponent) Render(msg chatMessage, sel bool) []CellChunk {
	lineCount := strings.Count(msg.ReasoningContent, "\n") + 1
	var chunks []CellChunk

	// Marker line: brackets in thinking yellow, text in dim gray
	markerStyle := DimStyle
	if sel {
		markerStyle = SelectionStyle
	}
	bracketStyle := ThinkingStyle
	if sel {
		bracketStyle = SelectionStyle
	}
	markerChunks := []CellChunk{CellChunk{Text: "[+]", Style: bracketStyle}}
	if !msg.ReasoningFolded {
		// Expanded: no standalone marker — merge [-] with first line below
		markerChunks = nil
	} else {
		markerChunks = []CellChunk{
			{Text: "[+]", Style: bracketStyle},
			{Text: fmt.Sprintf(" %d lines of reasoning", lineCount), Style: markerStyle},
		}
	}
	chunks = append(chunks, markerChunks...)

	if !msg.ReasoningFolded {
		contentStyle := ThinkingStyle
		if sel {
			contentStyle = SelectionStyle
		}
		// First line: bracket + first reasoning line on same row, no extra indent
		lines := strings.Split(msg.ReasoningContent, "\n")
		if len(lines) > 0 {
			markerText := "[-]"
			if firstLine := lines[0]; firstLine != "" {
				markerText = "[-] " + firstLine
			}
			chunks = append(chunks, CellChunk{Text: markerText, Style: bracketStyle})
		}
		// Remaining lines: indented 4 spaces
		for _, rLine := range lines[1:] {
			chunks = append(chunks, CellChunk{Text: "    " + rLine, Style: contentStyle})
		}
	}
	return chunks
}

// AnswerComponent renders structured Blocks (indented 4 spaces).
type AnswerComponent struct{}

func (AnswerComponent) Render(msg chatMessage, sel bool) []CellChunk {
	var chunks []CellChunk
	lastBlank := false
	for _, block := range msg.Blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			blockChunks := comp.Render(block, false)
			for _, bc := range blockChunks {
				// Deduplicate consecutive blank lines
				thisBlank := strings.TrimSpace(bc.Text) == ""
				if thisBlank && lastBlank {
					continue
				}
				lastBlank = thisBlank
				style := bc.Style
				if sel {
					style = SelectionStyle
				}
				chunks = append(chunks, CellChunk{Text: "    " + bc.Text, Style: style})
			}
		}
	}
	return chunks
}

// StreamingComponent renders raw markdown text during streaming.
type StreamingComponent struct{}

func (StreamingComponent) Render(msg chatMessage, sel bool) []CellChunk {
	label := "Response:"
	if msg.Streaming {
		label = "Response:"
	}
	labelStyle := ResponseLabel
	if sel {
		labelStyle = SelectionStyle
	}
	var chunks []CellChunk
	chunks = append(chunks, CellChunk{Text: label, Style: labelStyle})
	contentStyle := DefaultStyle
	if sel {
		contentStyle = SelectionStyle
	}
	chunks = append(chunks, CellChunk{Text: msg.Content, Style: contentStyle})
	return chunks
}

// ===== Block Components =====

// ParagraphComponent renders a paragraph with inline styling.
type ParagraphComponent struct{}

func (ParagraphComponent) Render(block ContentBlock, sel bool) []CellChunk {
	text := renderChunks(block.Chunks)
	if text == "" {
		return nil
	}
	plain := stripANSI(text)
	style := DefaultStyle
	if sel {
		style = SelectionStyle
	}
	return []CellChunk{{Text: plain, Style: style}}
}

// HeadingComponent renders a heading (bright white, bold, spacing).
type HeadingComponent struct{}

func (HeadingComponent) Render(block ContentBlock, sel bool) []CellChunk {
	text := renderChunks(block.Chunks)
	if text == "" {
		return nil
	}
	plain := stripANSI(text)
	style := HeadingStyle
	if sel {
		style = SelectionStyle
	}
	// blank line before, heading, blank line after
	return []CellChunk{
		{Text: "", Style: DefaultStyle},
		{Text: plain, Style: style},
		{Text: "", Style: DefaultStyle},
	}
}

// CodeComponent renders a code block (dim style).
type CodeComponent struct{}

func (CodeComponent) Render(block ContentBlock, sel bool) []CellChunk {
	code := block.Code
	if code == "" {
		return nil
	}
	code = strings.ReplaceAll(code, "	", "    ")
	style := DimStyle
	if sel {
		style = SelectionStyle
	}
	var chunks []CellChunk
	for _, codeLine := range strings.Split(code, "\n") {
		chunks = append(chunks, CellChunk{Text: "  " + codeLine, Style: style})
	}
	// Add trailing blank line for visual spacing after code blocks
	chunks = append(chunks, CellChunk{Text: "", Style: DefaultStyle})
	return chunks
}

// ListComponent renders ordered/unordered lists.
type ListComponent struct{}

func (ListComponent) Render(block ContentBlock, sel bool) []CellChunk {
	style := DefaultStyle
	if sel {
		style = SelectionStyle
	}
	var chunks []CellChunk
	for i, item := range block.Items {
		var prefix string
		if block.Numbered {
			prefix = fmt.Sprintf("  %d. ", i+1)
		} else {
			prefix = "  • "
		}
		if item.Type == "code" {
			for _, codeLine := range strings.Split(item.Code, "\n") {
				chunks = append(chunks, CellChunk{Text: prefix + codeLine, Style: DimStyle})
			}
			continue
		}
		text := renderChunks(item.Chunks)
		plain := stripANSI(text)
		if plain != "" {
			chunks = append(chunks, CellChunk{Text: prefix + plain, Style: style})
		}
	}
	return chunks
}

// QuoteComponent renders blockquote content.
type QuoteComponent struct{}

func (QuoteComponent) Render(block ContentBlock, sel bool) []CellChunk {
	style := DimStyle
	if sel {
		style = SelectionStyle
	}
	var chunks []CellChunk
	for _, item := range block.Items {
		text := renderChunks(item.Chunks)
		plain := stripANSI(text)
		if plain != "" {
			chunks = append(chunks, CellChunk{Text: "> " + plain, Style: style})
		}
	}
	return chunks
}

// HRComponent renders a horizontal rule.
type HRComponent struct{}

func (HRComponent) Render(block ContentBlock, sel bool) []CellChunk {
	rule := strings.Repeat("─", 40)
	style := DimStyle
	if sel {
		style = SelectionStyle
	}
	return []CellChunk{{Text: rule, Style: style}}
}

// TableComponent renders a table with box-drawing characters.
type TableComponent struct{}

func (TableComponent) Render(block ContentBlock, sel bool) []CellChunk {
	chunks := renderTable(block, sel)
	if sel {
		for i := range chunks {
			chunks[i].Style = SelectionStyle
		}
	}
	// Leading blank line for visual spacing before tables
	blank := CellChunk{Text: "", Style: DefaultStyle}
	return append([]CellChunk{blank}, append(chunks, blank)...)
}

// ===== Button Component =====

type ButtonComponent struct{}

func (ButtonComponent) Render(label string, indent int, sel bool) (string, int, int) {
	text := fmt.Sprintf("%s[ %s ]", strings.Repeat(" ", indent), label)
	col := indent + 1
	width := len(label) + 4
	if sel {
		return selectedStyle.Render(text), col, width
	}
	return dimStyle.Render(text), col, width
}
