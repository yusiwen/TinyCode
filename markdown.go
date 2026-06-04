package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
)

// renderMarkdown converts markdown text to ANSI-formatted terminal output using glamour.
func renderMarkdown(text string, style string) string {
	if text == "" {
		return text
	}
	if style == "" {
		style = "auto"
	}
	rendered, err := glamour.Render(text, style)
	if err != nil {
		return text // fallback to raw markdown on error
	}
	return strings.TrimRight(rendered, "\n")
}

// printMarkdown renders markdown text and prints it to stdout.
func printMarkdown(text string, style string) {
	rendered := renderMarkdown(text, style)
	fmt.Println(rendered)
}
