package screen

import (
	"fmt"
	"strconv"
	"strings"
)

// Render returns the screen as ANSI text: the same cells HTML() styles, encoded
// as SGR sequences instead of CSS.
//
// It is what makes a replayed stream round-trippable — feed the rendered text
// back into a fresh buffer and the plain text must be identical — and what lets
// a caller see the colours of a screen that was captured as a stream.
func (s *Buffer) Render() string {
	// Stop at the last row that has content: a trailing CRLF on a blank row would
	// scroll the screen by one line when the output is fed back into a terminal
	// (or into this emulator), which is exactly the round trip this must survive.
	last := -1
	for row := 0; row < s.height; row++ {
		for col := 0; col < s.width; col++ {
			cell := s.cells[s.idx(row, col)]
			if cell.r != 0 && cell.r != ' ' {
				last = row
				break
			}
		}
	}

	var b strings.Builder
	for row := 0; row <= last; row++ {
		if row > 0 {
			b.WriteString("\r\n")
		}
		end := s.width
		for end > 0 {
			cell := s.cells[s.idx(row, end-1)]
			if cell.r != 0 && cell.r != ' ' {
				break
			}
			end--
		}
		current := Style{}
		styled := false
		for col := 0; col < end; col++ {
			cell := s.cells[s.idx(row, col)]
			if cell.cont {
				continue
			}
			if cell.st != current {
				b.WriteString(sgrFor(cell.st))
				current, styled = cell.st, true
			}
			if cell.r == 0 {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(cell.r)
		}
		if styled {
			b.WriteString("\x1b[0m")
		}
		// CRLF, not LF: a bare LF leaves the cursor in the last column, which is
		// the same trap the renderer avoids when it paints a frame.
		_ = row
	}
	return b.String()
}

// sgrFor encodes a style as the SGR sequence that reproduces it, always with an
// explicit reset first so the sequence is independent of what came before.
func sgrFor(st Style) string {
	params := []string{"0"}
	if st.bold {
		params = append(params, "1")
	}
	if st.dim {
		params = append(params, "2")
	}
	if st.italic {
		params = append(params, "3")
	}
	if st.underline {
		params = append(params, "4")
	}
	if code, ok := fgCode(st.fg); ok {
		params = append(params, code)
	}
	if code, ok := bgCode(st.bg); ok {
		params = append(params, code)
	}
	return "\x1b[" + strings.Join(params, ";") + "m"
}

// cssToANSI maps every colour this package can produce back to its SGR index, so
// a round trip through Render() keeps the palette it started with.
var cssToANSI = func() map[string]int {
	m := make(map[string]int, 256)
	for i := 0; i < 256; i++ {
		m[xterm256ToCSS(i)] = i
	}
	// The 16 base colours are this package's palette, not the xterm defaults, so
	// they must win where the two disagree (otherwise a bright red comes back as
	// whichever cube entry happens to share its channel value).
	for i, css := range ansiPalette {
		m[css] = i
	}
	return m
}()

func fgCode(css string) (string, bool) { return colourCode(css, 30, 90, 38) }
func bgCode(css string) (string, bool) { return colourCode(css, 40, 100, 48) }

// colourCode encodes one colour: the 16 base entries as their own codes, the
// 256-colour cube as `38;5;N` / `48;5;N`, and anything else as truecolor.
func colourCode(css string, base, bright, extended int) (string, bool) {
	if css == "" {
		return "", false
	}
	if n, ok := cssToANSI[css]; ok {
		switch {
		case n < 8:
			return strconv.Itoa(base + n), true
		case n < 16:
			return strconv.Itoa(bright + n - 8), true
		default:
			return fmt.Sprintf("%d;5;%d", extended, n), true
		}
	}
	var r, g, b int
	if _, err := fmt.Sscanf(css, "#%02x%02x%02x", &r, &g, &b); err != nil {
		return "", false
	}
	return fmt.Sprintf("%d;2;%d;%d;%d", extended, r, g, b), true
}
