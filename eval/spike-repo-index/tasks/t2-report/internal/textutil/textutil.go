// Package textutil holds small string helpers shared by reports.
package textutil

import "strings"

// Truncate shortens s to at most n runes, appending an ellipsis when it cut.
func Truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// Title uppercases the first letter and leaves the rest alone.
func Title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// SplitLines splits on both CRLF and LF and drops a trailing empty line.
func SplitLines(s string) []string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		return lines[:n-1]
	}
	return lines
}
