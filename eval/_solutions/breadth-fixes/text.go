package task

import "strings"

// Title capitalises the first letter of every word and leaves the rest alone.
func Title(s string) string {
	words := strings.Split(s, " ")
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}
