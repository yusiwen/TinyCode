package golden

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// Clip truncates every row of an artifact to a terminal width, preserving the
// escape sequences that lead up to the cut and closing the styling at the end of
// each row.
//
// It exists because an application's *frame* may legitimately be wider than the
// terminal: the status bar in this project is drawn outside the cell grid, so an
// 80-column frame carries a 113-column status line, and a screenshot that trusted
// the frame would be as wide as the longest line (issue #25). A frame is therefore
// clipped before it is compared to a geometry or drawn, and the parity run against
// the harness is what surfaced the missing helper here.
//
// The truncation is `ansi.Truncate`, the same call the harness uses, so the two
// implementations cut in the same place.
func Clip(cols int, artifact string) string {
	if cols <= 0 {
		return artifact
	}
	rows := strings.Split(artifact, "\n")
	for i, row := range rows {
		rows[i] = ansi.Truncate(row, cols, "")
	}
	return strings.Join(rows, "\n")
}

// FitsClipped reports whether an artifact fits a geometry once each row has been
// clipped to it.
func FitsClipped(cols, rows int, artifact string) error {
	return Fits(cols, rows, Clip(cols, artifact))
}

// FitsWidth reports whether every row of an artifact fits a width, ignoring how many
// rows it has.
//
// A frame may be taller than the terminal — the terminal clips or scrolls it — but a
// row wider than the terminal is cut off mid-glyph, which is what issue #25 was
// about and what the harness asserts. Fits (both dimensions) stays the right check
// for an emulated screen, where the geometry is exact.
func FitsWidth(cols int, artifact string) error {
	if cols <= 0 {
		return fmt.Errorf("golden: geometry of %d columns is not a usable terminal width", cols)
	}
	plain := oscSequence.ReplaceAllString(artifact, "")
	plain = csiSequence.ReplaceAllString(plain, "")
	for i, row := range strings.Split(strings.TrimRight(plain, "\n"), "\n") {
		if width := runewidth.StringWidth(row); width > cols {
			return fmt.Errorf("golden: line %d is %d cells wide, width allows %d: %q", i+1, width, cols, row)
		}
	}
	return nil
}
