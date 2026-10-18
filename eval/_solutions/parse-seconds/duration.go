package task

import (
	"fmt"
	"strconv"
)

// ParseSeconds reads a duration made of one or more <number><unit> parts, where
// the unit is h, m or s, and returns it in seconds. "1h30m" is 5400.
func ParseSeconds(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("parse duration: empty")
	}
	total, i := 0, 0
	for i < len(s) {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if start == i {
			return 0, fmt.Errorf("parse duration %q: expected a number at %q", s, s[start:])
		}
		value, err := strconv.Atoi(s[start:i])
		if err != nil {
			return 0, fmt.Errorf("parse duration %q: %w", s, err)
		}
		if i >= len(s) {
			return 0, fmt.Errorf("parse duration %q: missing unit", s)
		}
		switch s[i] {
		case 'h':
			total += value * 3600
		case 'm':
			total += value * 60
		case 's':
			total += value
		default:
			return 0, fmt.Errorf("parse duration %q: unknown unit %q", s, s[i])
		}
		i++
	}
	return total, nil
}
