// Package validate checks the shape of values before they reach the store.
package validate

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// Errors a caller can tell apart.
var (
	ErrEmpty   = errors.New("validate: empty value")
	ErrTooLong = errors.New("validate: value too long")
)

// Limit is the longest value the service accepts.
const Limit = 512

// Value rejects empty, over-long and non-UTF8 strings.
func Value(s string) error {
	if strings.TrimSpace(s) == "" {
		return ErrEmpty
	}
	if !utf8.ValidString(s) {
		return ErrEmpty
	}
	if utf8.RuneCountInString(s) > Limit {
		return ErrTooLong
	}
	return nil
}
