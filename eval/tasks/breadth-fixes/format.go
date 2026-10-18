package task

import "strings"

// Pad right-pads s with spaces to exactly width characters, or returns s
// unchanged when it is already at least that long.
func Pad(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s)+1)
}
