package task

import "testing"

func TestParseSeconds(t *testing.T) {
	ok := map[string]int{"45s": 45, "2m": 120, "1h30m": 5400, "1h": 3600}
	for input, want := range ok {
		got, err := ParseSeconds(input)
		if err != nil {
			t.Errorf("ParseSeconds(%q) = error %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("ParseSeconds(%q) = %d, want %d", input, got, want)
		}
	}
	for _, bad := range []string{"", "10", "1x", "h"} {
		if _, err := ParseSeconds(bad); err == nil {
			t.Errorf("ParseSeconds(%q) = nil error, want one", bad)
		}
	}
}
