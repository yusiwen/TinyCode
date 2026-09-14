package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// welcomeInfo carries the startup statistics shown in the welcome banner.
type welcomeInfo struct {
	Tools  int
	Skills int
	Agents int
}

// welcomeArt is the ASCII wordmark for "TinyCode" (figlet "standard" style).
// Every row is exactly welcomeArtWidth columns wide so the glyphs stay aligned;
// the trailing padding is part of the artwork.
var welcomeArt = []string{
	" _____  _                 ____             _        ",
	"|_   _|(_) _ __   _   _  / ___|  ___    __| |  ___  ",
	"  | |  | || '_ \\ | | | || |     / _ \\  / _` | / _ \\ ",
	"  | |  | || | | || |_| || |___ | (_) || (_| ||  __/ ",
	"  |_|  |_||_| |_| \\__, | \\____| \\___/  \\__,_| \\___| ",
	"                  |___/                             ",
}

const (
	// welcomeArtWidth is the rendered width of the wordmark rows above.
	welcomeArtWidth = 52
	// welcomeMinWidth is the narrowest viewport that still gets the wordmark.
	// Narrower terminals fall back to a single-line title. The wordmark needs
	// welcomeArtWidth plus the indent, so only a small margin is required.
	welcomeMinWidth = welcomeArtWidth + 4
	// welcomeIndent is the left margin shared by every banner line.
	welcomeIndent = "  "
	// welcomeKeyWidth is the padded width of the command column.
	welcomeKeyWidth = 12
	// welcomeConfigPath is the configuration file advertised by the banner.
	welcomeConfigPath = "~/.tinycode/config.json"
	// welcomeSourceURL is the project home advertised by the banner. It is
	// rendered as an OSC 8 hyperlink (see welcomeSourceLink for the target).
	welcomeSourceURL = "github.com/yusiwen/TinyCode"
	// welcomeSourceLink is the clickable target; terminals require a scheme.
	welcomeSourceLink = "https://" + welcomeSourceURL
)

// welcomeCommand is one key/action row in the "Get started" block.
type welcomeCommand struct {
	Key  string
	Desc string
}

// welcomeCommands lists the startup shortcuts advertised by the banner.
var welcomeCommands = []welcomeCommand{
	{"/help", "Show all commands"},
	{"/model", "Switch provider / model"},
	{"/plan /build", "Switch agent mode"},
	{"Enter", "Send a message"},
	{"Ctrl+J", "New line in input"},
}

// newWelcomeMessage builds the startup system message. Content is a plain-text
// fallback used for clipboard copies; Banner drives the styled rendering.
func newWelcomeMessage(info welcomeInfo) chatMessage {
	return chatMessage{
		Role:    "system",
		Content: info.plainText(),
		Banner:  &info,
	}
}

// plainText renders the banner without styling, for copies and msg.Content.
func (info welcomeInfo) plainText() string {
	var b strings.Builder
	fmt.Fprintf(&b, "TinyCode — %d tools · %d skills · %d agents\n\n", info.Tools, info.Skills, info.Agents)
	b.WriteString("Get started:\n")
	for _, c := range welcomeCommands {
		fmt.Fprintf(&b, "  %-*s  %s\n", welcomeKeyWidth, c.Key, c.Desc)
	}
	fmt.Fprintf(&b, "\nConfig: %s\n", welcomeConfigPath)
	fmt.Fprintf(&b, "Source: %s", welcomeSourceURL)
	return b.String()
}

// renderWelcomeLines lays the banner out for a viewport of the given width.
// Every returned line is a ready-to-place row: callers must render lines with
// AppendInline (never word-wrap) so the ASCII art and the column alignment
// survive. A non-positive width means "unknown" and selects the full layout.
func renderWelcomeLines(info welcomeInfo, width int) [][]CellChunk {
	if width <= 0 {
		width = welcomeMinWidth + 1
	}

	var lines [][]CellChunk
	if width >= welcomeMinWidth {
		for _, row := range welcomeArt {
			lines = append(lines, []CellChunk{{Text: welcomeIndent + row, Style: bannerArtStyle}})
		}
	} else {
		lines = append(lines, []CellChunk{{Text: welcomeIndent + "TinyCode", Style: bannerArtStyle}})
	}

	lines = append(lines, nil)
	lines = appendWelcomeLine(lines, info.countChunks(), width)
	lines = append(lines, nil)
	lines = append(lines, []CellChunk{{Text: welcomeIndent + "Get started", Style: bannerAccentStyle}})
	for _, c := range welcomeCommands {
		lines = appendWelcomeLine(lines, commandChunks(c), width)
	}

	lines = append(lines, nil)
	lines = appendWelcomeLine(lines, footerChunks("Config", welcomeConfigPath, ""), width)
	lines = appendWelcomeLine(lines, footerChunks("Source", welcomeSourceURL, welcomeSourceLink), width)
	return lines
}

// countChunks renders "N tools · N skills · N agents" with the counters
// emphasized and the labels kept quiet.
func (info welcomeInfo) countChunks() []CellChunk {
	items := []struct {
		n     int
		label string
	}{
		{info.Tools, "tools"},
		{info.Skills, "skills"},
		{info.Agents, "agents"},
	}

	chunks := []CellChunk{{Text: welcomeIndent, Style: DefaultStyle}}
	for i, it := range items {
		if i > 0 {
			chunks = append(chunks, CellChunk{Text: "  ·  ", Style: DimStyle})
		}
		chunks = append(chunks,
			CellChunk{Text: fmt.Sprintf("%d", it.n), Style: bannerAccentStyle},
			CellChunk{Text: " " + it.label, Style: DefaultStyle},
		)
	}
	return chunks
}

// commandChunks renders one key/action row with the key column padded so the
// descriptions line up.
func commandChunks(c welcomeCommand) []CellChunk {
	key := c.Key
	if pad := welcomeKeyWidth - lipgloss.Width(key); pad > 0 {
		key += strings.Repeat(" ", pad)
	}
	return []CellChunk{
		{Text: welcomeIndent, Style: DefaultStyle},
		{Text: key, Style: bannerKeyStyle},
		{Text: "  ", Style: DefaultStyle},
		{Text: c.Desc, Style: DefaultStyle},
	}
}

// footerChunks renders a muted "Label  Value" row at the bottom of the banner.
// A non-empty link makes the value a clickable, underlined hyperlink.
func footerChunks(label, value, link string) []CellChunk {
	valueStyle := DefaultStyle
	if link != "" {
		valueStyle = bannerLinkStyle
		valueStyle.Link = link
	}
	return []CellChunk{
		{Text: welcomeIndent, Style: DimStyle},
		{Text: label + "  ", Style: DimStyle},
		{Text: value, Style: valueStyle},
	}
}

// appendWelcomeLine appends a styled line when it fits the viewport. A line
// that is too wide is degraded to plain text and word-wrapped so it never
// bleeds past the edge of a narrow terminal.
func appendWelcomeLine(lines [][]CellChunk, chunks []CellChunk, width int) [][]CellChunk {
	total := 0
	for _, c := range chunks {
		total += lipgloss.Width(c.Text)
	}
	if total <= width {
		return append(lines, chunks)
	}

	var b strings.Builder
	for _, c := range chunks {
		b.WriteString(c.Text)
	}
	for _, wc := range wordWrap(b.String(), width, DefaultStyle) {
		lines = append(lines, []CellChunk{wc})
	}
	return lines
}

// flattenWelcomeLines collapses the laid-out banner into one chunk per row for
// callers that render system messages chunk-per-row (no inline support).
func flattenWelcomeLines(lines [][]CellChunk) []CellChunk {
	out := make([]CellChunk, 0, len(lines))
	for _, line := range lines {
		if len(line) == 0 {
			out = append(out, CellChunk{Text: "", Style: DefaultStyle})
			continue
		}
		var b strings.Builder
		style := DefaultStyle
		for _, c := range line {
			b.WriteString(c.Text)
			if style == DefaultStyle {
				style = c.Style
			}
		}
		out = append(out, CellChunk{Text: b.String(), Style: style})
	}
	return out
}
