// Package screen is the smallest terminal emulator that can replay what a TUI's
// renderer writes to a real terminal.
//
// It exists so an artifact can be taken from the live stream instead of from the
// renderer's return value: a regression that only lives in the renderer — a
// repaint that never happens, a line that is never erased, cursor addressing
// that lands one row off — is invisible to a frame function but shows up here.
//
// The vocabulary is what a Bubble Tea-style renderer actually emits, measured
// from a captured 80x24 stream: `\r` and `\r\n` line breaks, `ESC[K` erases,
// `ESC[<n>A` cursor-up repaints, `ESC[6n` / `ESC[?25l` / `ESC[?2004h` /
// `ESC[?1003h` / `ESC[?1006h` mode changes, the `ESC]11;?` background query and
// SGR styling. Cursor-addressed writes are supported as well (`ESC[<n>;<m>H`,
// `ESC[<n>G`, `ESC[<n>J`) so a renderer change fails loudly instead of silently
// producing garbage.
package screen

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"
)

// a line that is never erased, cursor addressing that lands one row off - is
// invisible to the frame functions but shows up here.
//
// The vocabulary is what Bubble Tea's standard renderer actually emits, measured
// from a captured 80x24 welcome stream: `\r` and `\r\n` line breaks, `ESC[K`
// erases, `ESC[<n>A` cursor-up repaints, `ESC[6n` / `ESC[?25l` / `ESC[?2004h` /
// `ESC[?1003h` / `ESC[?1006h` mode changes, the `ESC]11;?` background query and
// SGR styling. Cursor-addressed writes are supported as well (`ESC[<n>;<m>H`,
// `ESC[<n>G`, `ESC[<n>J`) even though the current renderer does not need them,
// so a renderer change fails loudly instead of silently producing garbage.
type Buffer struct {
	width, height int
	cells         []Cell
	row, col      int
	pendingWrap   bool
	style         Style
}

// Cell is one character cell of the terminal.
type Cell struct {
	r    rune
	st   Style
	cont bool // trailing half of a double-width rune
}

func New(width, height int) *Buffer {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	return &Buffer{width: width, height: height, cells: make([]Cell, width*height)}
}

func (s *Buffer) idx(row, col int) int { return row*s.width + col }

// scrollUp drops the top row when the cursor moves past the last one.
func (s *Buffer) scrollUp() {
	if s.row < s.height {
		return
	}
	copy(s.cells, s.cells[s.width:])
	for i := (s.height - 1) * s.width; i < s.height*s.width; i++ {
		s.cells[i] = Cell{}
	}
	s.row = s.height - 1
}

func (s *Buffer) put(r rune) {
	if s.pendingWrap {
		s.pendingWrap = false
		s.col = 0
		s.row++
		s.scrollUp()
	}
	if s.row < 0 {
		s.row = 0
	}
	if s.col < 0 {
		s.col = 0
	}
	if s.col >= s.width {
		s.col = s.width - 1
	}
	w := runewidth.RuneWidth(r)
	if w < 1 {
		w = 1
	}
	s.cells[s.idx(s.row, s.col)] = Cell{r: r, st: s.style}
	if w == 2 && s.col+1 < s.width {
		s.cells[s.idx(s.row, s.col+1)] = Cell{cont: true, st: s.style}
	}
	if s.col+w >= s.width {
		s.col = s.width - 1
		s.pendingWrap = true
		return
	}
	s.col += w
}

// eraseLine clears part of one row: 0 = cursor to end, 1 = start to cursor,
// 2 = the whole row.
func (s *Buffer) eraseLine(row, mode int) {
	if row < 0 || row >= s.height {
		return
	}
	from, to := 0, s.width-1
	switch mode {
	case 0:
		from = s.col
	case 1:
		to = s.col
	}
	for c := from; c <= to; c++ {
		s.cells[s.idx(row, c)] = Cell{}
	}
}

// eraseDisplay clears part of the screen: 0 = cursor to end, 1 = start to
// cursor, 2 = everything (and the cursor goes home).
func (s *Buffer) eraseDisplay(mode int) {
	switch mode {
	case 2:
		for i := range s.cells {
			s.cells[i] = Cell{}
		}
		s.row, s.col, s.pendingWrap = 0, 0, false
	case 0:
		s.eraseLine(s.row, 0)
		for r := s.row + 1; r < s.height; r++ {
			s.cells[s.idx(r, 0)] = Cell{}
			for c := 0; c < s.width; c++ {
				s.cells[s.idx(r, c)] = Cell{}
			}
		}
	case 1:
		for r := 0; r < s.row; r++ {
			for c := 0; c < s.width; c++ {
				s.cells[s.idx(r, c)] = Cell{}
			}
		}
		s.eraseLine(s.row, 1)
	}
}

// Write replays a chunk of terminal output.
func (s *Buffer) Write(p []byte) (int, error) {
	for i := 0; i < len(p); {
		switch {
		case p[i] == 0x1b && i+1 < len(p) && p[i+1] == '[':
			j := i + 2
			for j < len(p) && !(p[j] >= 0x40 && p[j] <= 0x7e) {
				j++
			}
			if j >= len(p) {
				return len(p), nil // truncated sequence: ignore the tail
			}
			s.csi(string(p[i+2:j]), p[j])
			i = j + 1
		case p[i] == 0x1b && i+1 < len(p) && p[i+1] == ']':
			j := i + 2
			for j < len(p) && p[j] != 0x07 && !(p[j] == 0x1b && j+1 < len(p) && p[j+1] == '\\') {
				j++
			}
			switch {
			case j >= len(p):
				return len(p), nil
			case p[j] == 0x07:
				i = j + 1
			default:
				i = j + 2
			}
		case p[i] == 0x1b:
			i += 2 // two-byte escape (charset selection, keypad modes)
		case p[i] == '\r':
			s.col, s.pendingWrap = 0, false
			i++
		case p[i] == '\n':
			s.row++
			s.pendingWrap = false
			s.scrollUp()
			i++
		case p[i] == '\b':
			if s.col > 0 {
				s.col--
			}
			s.pendingWrap = false
			i++
		case p[i] == '\t':
			s.col = min((s.col/8+1)*8, s.width-1)
			i++
		case p[i] < 0x20:
			i++ // other control characters carry no visible state here
		default:
			r, size := utf8.DecodeRune(p[i:])
			if r == utf8.RuneError && size == 1 {
				i++
				continue
			}
			s.put(r)
			i += size
		}
	}
	return len(p), nil
}

// csi applies one CSI sequence.
func (s *Buffer) csi(raw string, final byte) {
	params := parseInts(raw)
	first := 0
	if len(params) > 0 {
		first = params[0]
	}
	switch final {
	case 'm':
		s.style = applySGR(params, s.style)
	case 'A':
		s.row = max(s.row-max(first, 1), 0)
		s.pendingWrap = false
	case 'B':
		s.row += max(first, 1)
		s.scrollUp()
		s.pendingWrap = false
	case 'C':
		s.col = min(s.col+max(first, 1), s.width-1)
		s.pendingWrap = false
	case 'D':
		s.col = max(s.col-max(first, 1), 0)
		s.pendingWrap = false
	case 'G':
		s.col = min(max(first, 1)-1, s.width-1)
		s.pendingWrap = false
	case 'H', 'f':
		row, col := 1, 1
		if len(params) > 0 {
			row = max(params[0], 1)
		}
		if len(params) > 1 {
			col = max(params[1], 1)
		}
		s.row = min(row-1, s.height-1)
		s.col = min(col-1, s.width-1)
		s.pendingWrap = false
	case 'J':
		s.eraseDisplay(first)
	case 'K':
		s.eraseLine(s.row, first)
	}
	// Everything else (mode set/reset, device status, window ops) changes no
	// cell content, which is all this buffer reproduces.
}

// String renders the visible screen as plain text with trailing blanks removed,
// the form the frame goldens use.
func (s *Buffer) String() string {
	var b strings.Builder
	for row := 0; row < s.height; row++ {
		var line strings.Builder
		for col := 0; col < s.width; col++ {
			cell := s.cells[s.idx(row, col)]
			if cell.cont {
				continue
			}
			if cell.r == 0 {
				line.WriteByte(' ')
				continue
			}
			line.WriteRune(cell.r)
		}
		b.WriteString(strings.TrimRight(line.String(), " "))
		b.WriteByte('\n')
	}
	return b.String()
}

// HTML renders the visible screen as the body of the screenshot page, keeping
// the styling the stream carried.
func (s *Buffer) HTML() string {
	var body strings.Builder
	for row := 0; row < s.height; row++ {
		// Right-trim before emitting spans so the image has no dead margin.
		end := s.width
		for end > 0 {
			cell := s.cells[s.idx(row, end-1)]
			if cell.r != 0 && cell.r != ' ' {
				break
			}
			end--
		}
		for col := 0; col < end; {
			cell := s.cells[s.idx(row, col)]
			if cell.cont {
				col++
				continue
			}
			run := make([]rune, 0, 8)
			style := cell.st
			for col < end {
				next := s.cells[s.idx(row, col)]
				if next.cont {
					col++
					continue
				}
				if next.st != style {
					break
				}
				if next.r == 0 {
					run = append(run, ' ')
				} else {
					run = append(run, next.r)
				}
				col++
			}
			text := html.EscapeString(string(run))
			if attr := style.styleAttr(); attr != "" {
				fmt.Fprintf(&body, `<span style="%s">%s</span>`, attr, text)
			} else {
				body.WriteString(text)
			}
		}
		body.WriteByte('\n')
	}
	return body.String()
}

// parseInts parses CSI parameters, treating an empty or malformed field as 0
// the way the SGR and cursor sequences here do.
func parseInts(raw string) []int {
	if raw == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ';' || r == ':' })
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		n := 0
		for _, r := range f {
			if r < '0' || r > '9' {
				n = 0
				break
			}
			n = n*10 + int(r-'0')
		}
		out = append(out, n)
	}
	return out
}

// --- Replay fidelity ------------------------------------------------------
