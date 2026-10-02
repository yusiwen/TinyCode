package screen

// The SGR (Select Graphic Rendition) state machine: it turns the escape
// sequences a TUI emits into a per-cell style, and offers the two colour
// lookups a renderer needs (basic/bright ANSI and the 256-colour cube).

import (
	"fmt"
	"strings"
)

type Style struct {
	bold, dim, italic, underline bool
	fg, bg                       string
}

// ansiPalette maps the 16 base colours to CSS.
var ansiPalette = []string{
	"#1c1c1c", "#d75f5f", "#87af5f", "#d7af5f", "#5f87d7", "#af87d7", "#5fafaf", "#d0d0d0",
	"#6c6c6c", "#ff8787", "#afd787", "#ffd787", "#87afff", "#d7afff", "#87d7d7", "#ffffff",
}

// xterm256ToCSS converts an xterm 256-colour index to a CSS colour.
func xterm256ToCSS(n int) string {
	switch {
	case n < 16:
		return ansiPalette[n]
	case n < 232:
		n -= 16
		r, g, b := n/36, (n/6)%6, n%6
		conv := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + v*40
		}
		return fmt.Sprintf("#%02x%02x%02x", conv(r), conv(g), conv(b))
	default:
		v := 8 + (n-232)*10
		return fmt.Sprintf("#%02x%02x%02x", v, v, v)
	}
}

// applySGR folds one escape sequence's parameters into the state.
func applySGR(params []int, st Style) Style {
	for i := 0; i < len(params); i++ {
		c := params[i]
		switch {
		case c == 0:
			st = Style{}
		case c == 1:
			st.bold = true
		case c == 2:
			st.dim = true
		case c == 3:
			st.italic = true
		case c == 4:
			st.underline = true
		case c >= 30 && c <= 37:
			st.fg = ansiPalette[c-30]
		case c >= 90 && c <= 97:
			st.fg = ansiPalette[c-90+8]
		case c >= 40 && c <= 47:
			st.bg = ansiPalette[c-40]
		case c >= 100 && c <= 107:
			st.bg = ansiPalette[c-100+8]
		case (c == 38 || c == 48) && i+1 < len(params):
			var colour string
			switch {
			case params[i+1] == 5 && i+2 < len(params):
				colour = xterm256ToCSS(params[i+2])
				i += 2
			case params[i+1] == 2 && i+4 < len(params):
				colour = fmt.Sprintf("#%02x%02x%02x", params[i+2], params[i+3], params[i+4])
				i += 4
			default:
				i++
				continue
			}
			if c == 38 {
				st.fg = colour
			} else {
				st.bg = colour
			}
		}
	}
	return st
}

// styleAttr renders the state as an inline CSS declaration, or "" when plain.
func (st Style) styleAttr() string {
	var parts []string
	if st.bold {
		parts = append(parts, "font-weight:700")
	}
	if st.dim {
		parts = append(parts, "opacity:.7")
	}
	if st.italic {
		parts = append(parts, "font-style:italic")
	}
	if st.underline {
		parts = append(parts, "text-decoration:underline")
	}
	if st.fg != "" {
		parts = append(parts, "color:"+st.fg)
	}
	if st.bg != "" {
		parts = append(parts, "background:"+st.bg)
	}
	return strings.Join(parts, ";")
}

// clipFrameToWidth cuts every row of a frame to the terminal's column count,
// which is what a real terminal receives: bubbletea's standard renderer applies
// `ansi.Truncate(line, r.width, "")` to each line before writing it, so a status
// bar wider than the window is cut there and never reaches the screen
// (standard_renderer.go, v1.3.10 — the same ansi package is used here on purpose).
// A scenario builds a model directly and calls View(), which skips that step, so a
// 112-column compression status line used to be rendered in full on a 40-column
// image (issue #25).
