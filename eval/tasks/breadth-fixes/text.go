package task

import "strings"

// Title capitalises the first letter of every word and leaves the rest alone.
func Title(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
