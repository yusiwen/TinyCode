package golden

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

// Fits reports whether an artifact fits a terminal geometry: no line wider than
// cols display cells and no more than rows lines.
//
// It exists because a screenshot or a golden is only evidence when its dimensions
// are pinned to the geometry it claims to show. The harness this came from once
// produced a 1912-pixel-wide "80 column" screenshot because the page had been
// allowed to widen to its longest line; the assertion is what makes that a
// failure instead of a surprise.
func Fits(cols, rows int, artifact string) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("golden: geometry %dx%d is not a usable terminal size", cols, rows)
	}
	// Escape sequences take no cells: a width measured on the raw stream counts
	// every byte of an SGR sequence as a character, which is how an 80-column frame
	// first reported itself as 101 cells wide.
	plain := oscSequence.ReplaceAllString(artifact, "")
	plain = csiSequence.ReplaceAllString(plain, "")
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	if strings.TrimSpace(plain) == "" {
		lines = nil
	}
	if len(lines) > rows {
		return fmt.Errorf("golden: artifact has %d lines, geometry allows %d", len(lines), rows)
	}
	for i, line := range lines {
		if width := runewidth.StringWidth(line); width > cols {
			return fmt.Errorf("golden: line %d is %d cells wide, geometry allows %d: %q", i+1, width, cols, line)
		}
	}
	return nil
}
