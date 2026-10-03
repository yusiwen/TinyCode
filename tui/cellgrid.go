package tui

import (
	"regexp"
	"strings"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// styleCache memoizes CellStyle → lipgloss.Style conversions.
var (
	styleCache = map[CellStyle]lipgloss.Style{}
	styleMu    sync.RWMutex
)

// CellStyle is a compact representation of character styling.
type CellStyle struct {
	Bold      bool
	Italic    bool
	Underline bool
	Fg        lipgloss.Color
	Bg        lipgloss.Color
	// Link is an OSC 8 hyperlink target (e.g. "https://example.com"). Empty
	// means plain text. Two runs are only merged when their Link is equal.
	Link string
}

// Cell is one visible character in the grid.
type Cell struct {
	Rune  rune
	Style CellStyle
	Width int // 1 for ASCII, 2 for CJK/emoji
}

// CellChunk is a run of plain text with a single style.
type CellChunk struct {
	Text  string
	Style CellStyle
}

// CellGrid is a virtual framebuffer: every visible cell in the viewport.
type CellGrid struct {
	cells []Cell
	width int
	rows  int
	col   int // current append column
	row   int // current append row
}

// Minimum grid geometry. A pty that was never given a window size reports 0x0
// to TIOCGWINSZ (verified with `stty size` on such a terminal), and a zero-area
// grid has no cell to write into, so its first Append panicked. Every caller may
// keep asking for whatever the terminal reported; the grid only refuses to be
// empty. The floor is one cell on purpose: a larger floor would make the model
// lay out for a width the renderer does not truncate to, and for a genuinely
// small terminal that is worse than a cramped but honest frame.
const (
	minTerminalWidth  = 1
	minTerminalHeight = 1
)

// gridWidth returns the cell width a grid will really have for a requested
// width, so callers can compare against it without repeating the clamp.
func gridWidth(width int) int {
	if width < minTerminalWidth {
		return minTerminalWidth
	}
	return width
}

// NewCellGrid creates a grid with the given width and initial height. A
// non-positive dimension is raised to the minimum (see minTerminalWidth).
func NewCellGrid(width, height int) *CellGrid {
	width = gridWidth(width)
	if height < minTerminalHeight {
		height = minTerminalHeight
	}
	return &CellGrid{
		cells: make([]Cell, width*height),
		width: width,
		rows:  height,
	}
}

// Reset clears the grid for a new frame.
func (g *CellGrid) Reset() {
	for i := range g.cells {
		g.cells[i] = Cell{}
	}
	g.col = 0
	g.row = 0
}

// RowCount returns the number of populated rows.
func (g *CellGrid) RowCount() int {
	return g.row
}

// RowText returns the plain text of a row.
func (g *CellGrid) RowText(row int) string {
	if row < 0 || row >= g.rows {
		return ""
	}
	var b strings.Builder
	skipUntil := 0
	for c := 0; c < g.width; c++ {
		if c < skipUntil {
			continue
		}
		cell := g.cells[g.cellIndex(row, c)]
		if cell.Rune != 0 {
			b.WriteRune(cell.Rune)
			if cell.Width > 1 {
				skipUntil = c + cell.Width
			}
		} else {
			break
		}
	}
	return b.String()
}

// cellIndex returns the flat index for (row, col).
func (g *CellGrid) cellIndex(row, col int) int {
	return row*g.width + col
}

// rowEmpty returns true if the row has no non-zero runes.
func (g *CellGrid) rowEmpty(row int) bool {
	if row < 0 || row >= g.rows {
		return true
	}
	for c := 0; c < g.width; c++ {
		if g.cells[g.cellIndex(row, c)].Rune != 0 {
			return false
		}
	}
	return true
}

// Set places a single cell at the current append position. Advances column.
// Automatically creates new rows when the current row fills up.
func (g *CellGrid) Append(runes []rune, style CellStyle) {
	for _, r := range runes {
		w := runewidth.RuneWidth(r)
		if w == 0 {
			w = 1 // treat zero-width chars as 1
		}
		// Wrap if this rune doesn't fit
		if g.col+w > g.width {
			g.col = 0
			g.row++
		}
		// Extend grid if needed
		g.ensureRow(g.row)
		idx := g.cellIndex(g.row, g.col)
		g.cells[idx] = Cell{Rune: r, Style: style, Width: w}
		g.col += w
	}
}

// AppendChunk places a CellChunk into the grid. Advances column and row.
func (g *CellGrid) AppendChunk(chunk CellChunk) {
	g.ensureRow(g.row)
	g.Append([]rune(chunk.Text), chunk.Style)
	g.col = 0
	g.row++
}

// AppendChunks places multiple chunks into the grid, each on its own line.
func (g *CellGrid) AppendChunks(chunks []CellChunk) {
	for _, c := range chunks {
		g.AppendChunk(c)
	}
}

// AppendInline places chunks on the same line (for multi-style single lines).
func (g *CellGrid) AppendInline(chunks []CellChunk) {
	for _, c := range chunks {
		g.Append([]rune(c.Text), c.Style)
	}
	g.col = 0
	g.row++
}

// ensureRow grows the grid if row index is beyond current size.
func (g *CellGrid) ensureRow(row int) {
	for row >= g.rows {
		g.cells = append(g.cells, make([]Cell, g.width)...)
		g.rows++
	}
}

// Get returns the cell at (row, col). Returns empty Cell if out of range.
func (g *CellGrid) Get(row, col int) Cell {
	if row < 0 || row >= g.rows || col < 0 || col >= g.width {
		return Cell{}
	}
	return g.cells[g.cellIndex(row, col)]
}

// Fill sets all cells in a rectangular range to a given style.
// Used for selection highlighting.
func (g *CellGrid) Fill(startRow, startCol, endRow, endCol int, style CellStyle) {
	for r := startRow; r <= endRow && r < g.rows; r++ {
		cStart := startCol
		cEnd := endCol
		if r > startRow {
			cStart = 0
		}
		if r < endRow {
			cEnd = g.width - 1
		}
		for c := cStart; c <= cEnd && c < g.width; c++ {
			idx := g.cellIndex(r, c)
			if g.cells[idx].Rune != 0 {
				g.cells[idx].Style = style
			}
		}
	}
}

// ExtractText returns the plain text within a rectangular cell range.
func (g *CellGrid) ExtractText(startRow, startCol, endRow, endCol int) string {
	var b strings.Builder
	for r := startRow; r <= endRow && r < g.rows; r++ {
		cStart := startCol
		cEnd := endCol
		if r > startRow {
			cStart = 0
		}
		if r < endRow {
			cEnd = g.width - 1
		}
		skipUntil := 0
		var lineBuf strings.Builder
		for c := cStart; c <= cEnd && c < g.width; c++ {
			if c < skipUntil {
				continue
			}
			cell := g.cells[g.cellIndex(r, c)]
			if cell.Rune != 0 {
				lineBuf.WriteRune(cell.Rune)
				if cell.Width > 1 {
					skipUntil = c + cell.Width
				}
			} else {
				lineBuf.WriteByte(' ')
			}
		}
		line := strings.TrimRight(lineBuf.String(), " ")
		b.WriteString(line)
		if r < endRow {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// Render produces an ANSI-formatted string for the viewport.
func (g *CellGrid) Render() string {
	var b strings.Builder
	// Find last non-empty row to skip trailing blanks
	lastRow := g.row - 1
	for lastRow >= 0 && g.rowEmpty(lastRow) {
		lastRow--
	}
	for r := 0; r <= lastRow; r++ {
		col := 0
		for col < g.width {
			cell := g.cells[g.cellIndex(r, col)]
			if cell.Rune == 0 {
				b.WriteByte(' ')
				col++
				continue
			}
			// Find continuous run of same-style cells
			style := cell.Style
			var text strings.Builder
			for col < g.width {
				c := g.cells[g.cellIndex(r, col)]
				if c.Rune == 0 || c.Style != style {
					break
				}
				text.WriteRune(c.Rune)
				col += c.Width
			}
			// Render styled segment
			rendered := renderStyled(style, text.String())
			if style.Link != "" {
				rendered = hyperlink(style.Link, rendered)
			}
			b.WriteString(rendered)
		}
		if r < g.rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// styleToLipgloss converts CellStyle to a lipgloss.Style for rendering.
// Results are cached, so repeated calls for the same style are fast.
// sgrSequence matches one SGR sequence so its parameters can be rewritten.
var sgrSequence = regexp.MustCompile("\x1b\\[([0-9;]*)m")

// renderStyled renders text in a cell style.
//
// Everything goes through lipgloss unchanged — the committed ANSI golden stores its
// bytes — except for hyperlinked text, where lipgloss v1.1.0 emits the underline
// parameter twice (`\x1b[4;4m`, and `\x1b[4;38;2;0;255;255;4m` for the banner link).
// A terminal ignores the repeat, but it doubles the bytes of every character of the
// link, and one escape per character is what made the welcome screen's Source row
// unreadable in a capture (issue #80). Hyperlinks get one well-formed sequence instead.
func renderStyled(s CellStyle, text string) string {
	out := styleToLipgloss(s).Render(text)
	if s.Link == "" {
		return out
	}
	return sgrSequence.ReplaceAllStringFunc(out, func(seq string) string {
		params := strings.Split(seq[2:len(seq)-1], ";")
		seen := make(map[string]bool, len(params))
		kept := params[:0]
		for _, p := range params {
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			kept = append(kept, p)
		}
		return "\x1b[" + strings.Join(kept, ";") + "m"
	})
}

func styleToLipgloss(s CellStyle) lipgloss.Style {
	styleMu.RLock()
	if cached, ok := styleCache[s]; ok {
		styleMu.RUnlock()
		return cached
	}
	styleMu.RUnlock()

	styleMu.Lock()
	// Double-check after write lock
	if cached, ok := styleCache[s]; ok {
		styleMu.Unlock()
		return cached
	}
	ls := lipgloss.NewStyle()
	if s.Bold {
		ls = ls.Bold(true)
	}
	if s.Italic {
		ls = ls.Italic(true)
	}
	if s.Underline {
		ls = ls.Underline(true)
	}
	if s.Fg != "" {
		ls = ls.Foreground(s.Fg)
	}
	if s.Bg != "" {
		ls = ls.Background(s.Bg)
	}
	styleCache[s] = ls
	styleMu.Unlock()
	return ls
}

// --- Word wrapping ---

// wordWrap splits text into CellChunks, each no wider than maxWidth.
// Preserves existing newlines as chunk boundaries.
// Returns one CellChunk per wrapped line.
func wordWrap(text string, maxWidth int, style CellStyle) []CellChunk {
	if maxWidth < 1 {
		maxWidth = 1
	}
	var chunks []CellChunk
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			chunks = append(chunks, CellChunk{Text: "", Style: style})
			continue
		}
		// Preserve leading spaces (indent)
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		indentStr := line[:indent]
		// No text after indent → output indent only
		if trimmed == "" {
			chunks = append(chunks, CellChunk{Text: indentStr, Style: style})
			continue
		}

		// Split into words and the whitespace runs between them. Collapsing the
		// runs (strings.Fields) destroyed the alignment of code blocks, so the
		// interior spacing is preserved and only dropped at a wrap point.
		type token struct {
			text  string
			space bool
		}
		var tokens []token
		for i := 0; i < len(trimmed); {
			j := i
			if trimmed[i] == ' ' {
				for j < len(trimmed) && trimmed[j] == ' ' {
					j++
				}
				tokens = append(tokens, token{trimmed[i:j], true})
			} else {
				for j < len(trimmed) && trimmed[j] != ' ' {
					j++
				}
				tokens = append(tokens, token{trimmed[i:j], false})
			}
			i = j
		}

		var lineBuilder strings.Builder
		lineWidth := indent
		hasContent := false
		var pendingSpace string
		chunksBefore := len(chunks)
		flush := func() {
			if hasContent {
				chunks = append(chunks, CellChunk{Text: indentStr + lineBuilder.String(), Style: style})
			}
			lineBuilder.Reset()
			lineWidth = indent
			hasContent = false
		}

		for _, tk := range tokens {
			if tk.space {
				// Hold the run; it is emitted only if another word follows on
				// the same line, so lines never end in trailing blanks.
				pendingSpace = tk.text
				continue
			}
			tw := runewidth.StringWidth(tk.text)
			pw := runewidth.StringWidth(pendingSpace)
			if hasContent && lineWidth+pw+tw > maxWidth {
				flush()
				pendingSpace = ""
				pw = 0
			}
			if hasContent && pendingSpace != "" {
				lineBuilder.WriteString(pendingSpace)
				lineWidth += pw
			}
			pendingSpace = ""
			lineBuilder.WriteString(tk.text)
			lineWidth += tw
			hasContent = true
		}
		flush()
		// A line that produced no content (only indent) still needs a chunk so
		// the row count stays stable.
		if len(chunks) == chunksBefore && indentStr != "" {
			chunks = append(chunks, CellChunk{Text: indentStr, Style: style})
		}
	}
	return chunks
}

// hyperlink wraps text in an OSC 8 hyperlink sequence, which makes the text
// clickable (Cmd/Ctrl+click) in terminals that support it. Terminals without
// support ignore the sequence and render the styled text unchanged, so this is
// always safe to emit.
func hyperlink(url, text string) string {
	if url == "" {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// --- Theme-aware style constructors (updated by ApplyTheme) ---

var (
	DefaultStyle   CellStyle
	ThinkingStyle  CellStyle
	ResponseLabel  CellStyle
	HeadingStyle   CellStyle
	DimStyle       CellStyle
	SelectionStyle CellStyle
	UserStyle      CellStyle
	CodeStyle      CellStyle
	SystemStyle    CellStyle
	StatusBarStyle CellStyle

	// Welcome banner styles (see welcome.go)
	bannerArtStyle    CellStyle
	bannerAccentStyle CellStyle
	bannerKeyStyle    CellStyle
	bannerLinkStyle   CellStyle
)
