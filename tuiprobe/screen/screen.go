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
	// generation counts the writes this buffer has replayed, and gen records, per
	// cell, the generation that last wrote it. A screen alone cannot tell a second
	// run of a program from the first — both draw the same frame — so the age of
	// each cell is what makes "text drawn since this moment" answerable (issue #126).
	generation uint64
	gen        []uint64
	// pending holds an escape sequence that was split across Write calls. Terminal
	// output arrives in arbitrary chunks, and a chunk boundary can fall inside an
	// SGR or an OSC sequence: without this, the leading half was dropped and the
	// trailing half was drawn as text ("github.com/yusiw5;255;4men/Tin", issue #75).
	pending []byte
}

// Cell is one character cell of the terminal.
type Cell struct {
	r     rune
	Style Style
	cont  bool // trailing half of a double-width rune
}

func New(width, height int) *Buffer {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	return &Buffer{
		width: width, height: height,
		cells: make([]Cell, width*height),
		gen:   make([]uint64, width*height),
	}
}

// Reset re-lays-out the buffer for a new geometry, dropping what was on it but
// keeping the write counter.
//
// Keeping it matters: a mark recorded before a resize must stay comparable, or a
// wait "since" it would either match nothing for ever (a fresh counter behind the
// mark) or match the old screen again (a counter reset to zero).
func (b *Buffer) Reset(width, height int) {
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	b.width, b.height = width, height
	b.cells = make([]Cell, width*height)
	b.gen = make([]uint64, width*height)
	b.row, b.col, b.pendingWrap, b.style = 0, 0, false, Style{}
	b.pending = nil
}

// Generation is how many writes this buffer has replayed. Record it to ask later,
// with StringSince, what has been drawn in the meantime.
func (b *Buffer) Generation() uint64 { return b.generation }

func (s *Buffer) idx(row, col int) int { return row*s.width + col }

// clear blanks the cells with row-major indices in [from, to) and records that the
// blank is their newest state.
func (s *Buffer) clear(from, to int) {
	if from < 0 {
		from = 0
	}
	if to > len(s.cells) {
		to = len(s.cells)
	}
	for i := from; i < to; i++ {
		s.cells[i] = Cell{}
		s.gen[i] = s.generation
	}
}

// scrollUp drops the top row when the cursor moves past the last one.
func (s *Buffer) scrollUp() {
	if s.row < s.height {
		return
	}
	copy(s.cells, s.cells[s.width:])
	copy(s.gen, s.gen[s.width:])
	s.clear((s.height-1)*s.width, s.height*s.width)
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
	lead := s.idx(s.row, s.col)
	s.cells[lead] = Cell{r: r, Style: s.style}
	s.gen[lead] = s.generation
	if w == 2 && s.col+1 < s.width {
		cont := s.idx(s.row, s.col+1)
		s.cells[cont] = Cell{cont: true, Style: s.style}
		s.gen[cont] = s.generation
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
		s.clear(s.idx(row, c), s.idx(row, c)+1)
	}
}

// eraseDisplay clears part of the screen: 0 = cursor to end, 1 = start to
// cursor, 2 = everything (and the cursor goes home).
func (s *Buffer) eraseDisplay(mode int) {
	switch mode {
	case 2:
		s.clear(0, len(s.cells))
		s.row, s.col, s.pendingWrap = 0, 0, false
	case 0:
		s.eraseLine(s.row, 0)
		for r := s.row + 1; r < s.height; r++ {
			s.clear(s.idx(r, 0), s.idx(r, 0)+s.width)
		}
	case 1:
		for r := 0; r < s.row; r++ {
			s.clear(s.idx(r, 0), s.idx(r, 0)+s.width)
		}
		s.eraseLine(s.row, 1)
	}
}

// maxPendingBounds how much of an unfinished escape sequence is kept. Anything longer
// is a malformed stream, and keeping it forever would be a slow leak.
const maxPending = 4096

// stash keeps an unfinished escape sequence for the next Write, up to a bound.
func (s *Buffer) stash(tail []byte) {
	if len(tail) > maxPending {
		return
	}
	s.pending = append(s.pending[:0], tail...)
}

// Write replays a chunk of terminal output.
func (s *Buffer) Write(p []byte) (int, error) {
	if len(s.pending) > 0 {
		joined := make([]byte, 0, len(s.pending)+len(p))
		joined = append(joined, s.pending...)
		joined = append(joined, p...)
		p = joined
		s.pending = nil
	}
	if len(p) > 0 {
		// One chunk is one moment: everything it draws shares a generation, and a
		// caller's mark either precedes the whole chunk or follows it.
		s.generation++
	}
	for i := 0; i < len(p); {
		switch {
		case p[i] == 0x1b && i+1 >= len(p):
			s.stash(p[i:])
			return len(p), nil
		case p[i] == 0x1b && p[i+1] == '[':
			j := i + 2
			for j < len(p) && !(p[j] >= 0x40 && p[j] <= 0x7e) {
				j++
			}
			if j >= len(p) {
				s.stash(p[i:])
				return len(p), nil
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
				s.stash(p[i:])
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
func (s *Buffer) String() string { return s.StringSince(0) }

// StringSince renders like String, but only from the cells last written *after* gen:
// everything the screen held at that point reads as blank.
//
// A pattern that matches this text therefore comes from output the program produced
// after the generation was recorded, which is what lets a scenario tell a second run
// of a program from the first run's leftover frame (issue #126).
func (s *Buffer) StringSince(gen uint64) string {
	var b strings.Builder
	for row := 0; row < s.height; row++ {
		var line strings.Builder
		for col := 0; col < s.width; col++ {
			cell := s.cells[s.idx(row, col)]
			if cell.cont {
				continue
			}
			if cell.r == 0 || s.gen[s.idx(row, col)] <= gen {
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
			style := cell.Style
			for col < end {
				next := s.cells[s.idx(row, col)]
				if next.cont {
					col++
					continue
				}
				if next.Style != style {
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

// Bounds is the terminal geometry the buffer was created with.
func (b *Buffer) Bounds() (cols, rows int) { return b.width, b.height }

// Cursor is the cursor position, zero-based: what a terminal reports for CSI 6n.
func (b *Buffer) Cursor() (col, row int) { return b.col, b.row }

// CellAt returns the rune and style at a position, and whether the cell holds a
// character at all. A blank cell reports rune 0 so a renderer can tell "space"
// from "nothing was ever drawn here".
//
// The trailing half of a wide rune reports ok=false: a renderer draws the rune
// once, at its leading cell.
func (b *Buffer) CellAt(col, row int) (r rune, style Style, ok bool) {
	if col < 0 || row < 0 || col >= b.width || row >= b.height {
		return 0, Style{}, false
	}
	cell := b.cells[b.idx(row, col)]
	if cell.cont {
		return 0, cell.Style, false
	}
	return cell.r, cell.Style, cell.r != 0
}
