package task

import "testing"

func TestToRoman(t *testing.T) {
	cases := map[int]string{
		1:    "I",
		4:    "IV",
		9:    "IX",
		40:   "XL",
		1994: "MCMXCIV",
		3999: "MMMCMXCIX",
	}
	for n, want := range cases {
		if got := ToRoman(n); got != want {
			t.Errorf("ToRoman(%d) = %q, want %q", n, got, want)
		}
	}
}
