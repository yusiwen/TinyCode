package task

import (
	"math"
	"testing"
)

func TestToFahrenheit(t *testing.T) {
	cases := map[int]float64{0: 32, 100: 212, 37: 98.6, -17: 1.4}
	for c, want := range cases {
		got := ToFahrenheit(c)
		if math.Abs(got-want) > 0.01 {
			t.Errorf("ToFahrenheit(%d) = %v, want %v", c, got, want)
		}
	}
}
