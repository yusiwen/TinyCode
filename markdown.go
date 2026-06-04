package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
)

// renderMarkdown converts markdown text to ANSI-formatted terminal output using glamour.
func renderMarkdown(text string) string {
	if text == "" {
		return text
	}
	// Auto-detect style from terminal, fall back to dark
	rendered, err := glamour.Render(text, "auto")
	if err != nil {
		return text // fallback to raw markdown on error
	}
	return strings.TrimRight(rendered, "\n")
}

// printMarkdown renders markdown text and prints it to stdout.
func printMarkdown(text string) {
	rendered := renderMarkdown(text)
	fmt.Println(rendered)
}
