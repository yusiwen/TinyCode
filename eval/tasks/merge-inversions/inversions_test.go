package task

import (
	"testing"
	"time"
)

func TestCountInversionsSmall(t *testing.T) {
	cases := []struct {
		xs   []int
		want int64
	}{
		{[]int{2, 1}, 1},
		{[]int{1, 2, 3}, 0},
		{[]int{3, 2, 1}, 3},
		{[]int{1, 3, 2, 4}, 1},
		{nil, 0},
		{[]int{5}, 0},
	}
	for _, tc := range cases {
		if got := CountInversions(tc.xs); got != tc.want {
			t.Errorf("CountInversions(%v) = %d, want %d", tc.xs, got, tc.want)
		}
	}
}

// TestCountInversionsIsFastEnough pins the complexity the task asks for: the
// answer is the same either way, and only the clock can tell them apart.
func TestCountInversionsIsFastEnough(t *testing.T) {
	const n = 200_000
	xs := make([]int, n)
	for i := range xs {
		xs[i] = n - i
	}
	start := time.Now()
	got := CountInversions(xs)
	elapsed := time.Since(start)

	want := int64(n) * int64(n-1) / 2
	if got != want {
		t.Fatalf("CountInversions of a reversed slice of %d = %d, want %d", n, got, want)
	}
	if elapsed > time.Second {
		t.Fatalf("counting took %v; the suite expects an O(n log n) implementation (the reference takes milliseconds)", elapsed)
	}
}
