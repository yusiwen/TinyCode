package task

import "testing"

func TestReverseWords(t *testing.T) {
	cases := map[string]string{
		"the quick brown fox": "fox brown quick the",
		"one":                 "one",
		"":                    "",
		"a  b":                "b a",
	}
	for input, want := range cases {
		if got := ReverseWords(input); got != want {
			t.Errorf("ReverseWords(%q) = %q, want %q", input, got, want)
		}
	}
}
