package task

import "strings"

// ReverseWords returns the words of s in reverse order, separated by single
// spaces. s may be empty, and its words may be separated by runs of spaces.
func ReverseWords(s string) string {
	fields := strings.Fields(s)
	for i, j := 0, len(fields)-1; i < j; i, j = i+1, j-1 {
		fields[i], fields[j] = fields[j], fields[i]
	}
	return strings.Join(fields, " ")
}
