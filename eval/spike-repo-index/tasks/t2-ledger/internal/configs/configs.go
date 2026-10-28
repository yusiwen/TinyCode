// Package configs reads key=value settings from a list of strings.
package configs

import (
	"errors"
	"strconv"
	"strings"
)

// ErrMalformed reports a line that is not key=value.
var ErrMalformed = errors.New("configs: expected key=value")

// Parse reads key=value lines into a map. Blank lines and lines starting with #
// are ignored.
func Parse(lines []string) (map[string]string, error) {
	out := map[string]string{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, ErrMalformed
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out, nil
}

// Int reads an integer setting, falling back when it is absent.
func Int(settings map[string]string, key string, fallback int) int {
	raw, ok := settings[key]
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}
