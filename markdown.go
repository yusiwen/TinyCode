package main

import (
	"fmt"
	"strings"
)

// ANSI color codes for markdown formatting
const (
	ansiBold      = "\033[1m"
	ansiDim       = "\033[2m"
	ansiCyan      = "\033[36m"
	ansiYellow    = "\033[33m"
	ansiBlue      = "\033[34m"
	ansiGreen     = "\033[32m"
	ansiMagenta   = "\033[35m"
	ansiRed       = "\033[31m"
	ansiReset     = "\033[0m"
)

// renderMarkdown converts basic markdown to ANSI-formatted terminal output.
// Handles: headings, bold, inline code, code blocks, horizontal rules, lists.
func renderMarkdown(text string) string {
	if text == "" {
		return text
	}

	lines := strings.Split(text, "\n")
	var out strings.Builder
	inCodeBlock := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Code block fence
		if strings.HasPrefix(trimmed, "```") {
			if inCodeBlock {
				out.WriteString(ansiReset + "\n")
				inCodeBlock = false
			} else {
				out.WriteString(ansiDim)
				inCodeBlock = true
			}
			out.WriteString("\n")
			continue
		}

		// Inside code block
		if inCodeBlock {
			out.WriteString("  " + line + "\n")
			continue
		}

		// Horizontal rule (---)
		if strings.TrimLeft(trimmed, "-") == "" && len(trimmed) >= 3 {
			out.WriteString(ansiDim + strings.Repeat("─", 50) + ansiReset + "\n")
			continue
		}

		// Table separator (|---|---|)
		if strings.Contains(trimmed, "|---") {
			continue // skip table separator lines
		}

		// Heading ###
		if strings.HasPrefix(line, "### ") {
			out.WriteString(ansiBold + ansiCyan + "▎ " + line[4:] + ansiReset + "\n")
			continue
		}
		if strings.HasPrefix(line, "## ") {
			out.WriteString(ansiBold + ansiBlue + "▎ " + line[3:] + ansiReset + "\n")
			continue
		}
		if strings.HasPrefix(line, "# ") {
			out.WriteString(ansiBold + ansiYellow + "▎ " + line[2:] + ansiReset + "\n")
			continue
		}

		// Unordered list
		if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") {
			content := strings.TrimPrefix(strings.TrimPrefix(trimmed, "- "), "* ")
			out.WriteString("  " + ansiGreen + "•" + ansiReset + " " + formatInline(content) + "\n")
			continue
		}

		// Ordered list
		if len(trimmed) > 0 && trimmed[0] >= '0' && trimmed[0] <= '9' {
			if idx := strings.Index(trimmed, ". "); idx > 0 && idx < 4 {
				content := trimmed[idx+2:]
				out.WriteString("  " + formatInline(content) + "\n")
				continue
			}
		}

		// Table row (starts with |)
		if strings.HasPrefix(trimmed, "|") {
			cells := strings.Split(trimmed, "|")
			var formatted []string
			for _, cell := range cells {
				c := strings.TrimSpace(cell)
				if c != "" {
					formatted = append(formatted, formatInline(c))
				}
			}
			if len(formatted) > 0 {
				out.WriteString(ansiDim + "│" + ansiReset)
				for _, f := range formatted {
					out.WriteString(" " + f + " ")
					out.WriteString(ansiDim + "│" + ansiReset)
				}
				out.WriteString("\n")
			} else {
				out.WriteString("\n")
			}
			continue
		}

		// Blockquote
		if strings.HasPrefix(trimmed, "> ") {
			out.WriteString(ansiDim + "┃ " + ansiReset + formatInline(trimmed[2:]) + "\n")
			continue
		}

		// Empty line
		if trimmed == "" {
			out.WriteString("\n")
			continue
		}

		// Normal paragraph
		out.WriteString(formatInline(trimmed) + "\n")
	}

	return strings.TrimRight(out.String(), "\n")
}

// formatInline handles **bold**, `inline code`, and [links](url)
func formatInline(text string) string {
	// Bold: **text** or __text__
	result := text

	// Process inline code first (to avoid conflicts)
	result = processInlineCode(result)

	// Bold
	result = processBold(result)

	// Italic (single asterisk, not bold)
	result = processItalic(result)

	// Remove link syntax: [text](url) → text
	result = processLinks(result)

	return result
}

func processInlineCode(text string) string {
	var sb strings.Builder
	i := 0
	inCode := false
	for i < len(text) {
		if text[i] == '`' {
			if inCode {
				sb.WriteString(ansiReset)
				inCode = false
			} else {
				sb.WriteString(ansiGreen + ansiDim)
				inCode = true
			}
			i++
		} else {
			sb.WriteByte(text[i])
			i++
		}
	}
	if inCode {
		sb.WriteString(ansiReset)
	}
	return sb.String()
}

func processBold(text string) string {
	// Replace **text** with ANSI bold
	var sb strings.Builder
	i := 0
	for i < len(text) {
		if i+1 < len(text) && text[i] == '*' && text[i+1] == '*' {
			// Toggle bold
			sb.WriteString(ansiBold)
			i += 2
			// Find closing **
			start := i
			for i < len(text) {
				if i+1 < len(text) && text[i] == '*' && text[i+1] == '*' {
					sb.WriteString(text[start:i])
					sb.WriteString(ansiReset)
					i += 2
					break
				}
				i++
			}
			if i >= len(text) {
				sb.WriteString(text[start:])
			}
		} else {
			sb.WriteByte(text[i])
			i++
		}
	}
	return sb.String()
}

func processItalic(text string) string {
	// Handle single *text* (but not **)
	var sb strings.Builder
	i := 0
	for i < len(text) {
		if text[i] == '*' {
			// Check if this is bold (**) - if so, pass through
			if i+1 < len(text) && text[i+1] == '*' {
				sb.WriteString("**")
				i += 2
				continue
			}
			// Single asterisk — toggle italic-like (use dim)
			sb.WriteString(ansiDim)
			i++
			start := i
			for i < len(text) {
				if text[i] == '*' {
					sb.WriteString(text[start:i])
					sb.WriteString(ansiReset)
					i++
					break
				}
				i++
			}
			if i >= len(text) {
				sb.WriteString(text[start:])
			}
		} else {
			sb.WriteByte(text[i])
			i++
		}
	}
	return sb.String()
}

func processLinks(text string) string {
	// [text](url) → text (remove link)
	var sb strings.Builder
	i := 0
	for i < len(text) {
		if text[i] == '[' {
			// Find ]
			closeBracket := strings.IndexByte(text[i:], ']')
			if closeBracket > 0 && i+closeBracket+1 < len(text) && text[i+closeBracket+1] == '(' {
				// Find )
				closeParen := strings.IndexByte(text[i+closeBracket+1:], ')')
				if closeParen > 0 {
					linkText := text[i+1 : i+closeBracket]
					sb.WriteString(linkText)
					i += closeBracket + closeParen + 2
					continue
				}
			}
		}
		sb.WriteByte(text[i])
		i++
	}
	return sb.String()
}

// printMarkdown renders markdown text and prints it to stdout.
func printMarkdown(text string) {
	rendered := renderMarkdown(text)
	fmt.Println(rendered)
}
