package task

import "testing"

func TestCount(t *testing.T) {
	cases := []struct {
		xs        []int
		threshold int
		want      int
	}{
		{[]int{1, 5, 10}, 5, 2},
		{[]int{5}, 5, 1},
		{nil, 0, 0},
		{[]int{1, 2, 3}, 10, 0},
	}
	for _, tc := range cases {
		if got := Count(tc.xs, tc.threshold); got != tc.want {
			t.Errorf("Count(%v, %d) = %d, want %d", tc.xs, tc.threshold, got, tc.want)
		}
	}
}
