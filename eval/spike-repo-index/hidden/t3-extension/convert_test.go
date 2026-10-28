package task

import "testing"

func TestToCelsius(t *testing.T) {
	cases := map[float64]int{32: 0, 212: 100, 98.6: 37, 0: -17}
	for f, want := range cases {
		if got := ToCelsius(f); got != want {
			t.Errorf("ToCelsius(%v) = %d, want %d", f, got, want)
		}
	}
}
