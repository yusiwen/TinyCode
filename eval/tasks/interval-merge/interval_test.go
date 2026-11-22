package task

import (
	"reflect"
	"testing"
)

func iv(start, end int64, startInclusive, endInclusive bool) Interval {
	return Interval{
		Start: start, End: end,
		StartInclusive: startInclusive, EndInclusive: endInclusive,
	}
}

func TestMerge(t *testing.T) {
	cases := []struct {
		name string
		in   []Interval
		want []Interval
	}{
		{
			name: "closed intervals sharing an endpoint merge",
			in:   []Interval{iv(1, 2, true, true), iv(2, 3, true, true)},
			want: []Interval{iv(1, 3, true, true)},
		},
		{
			name: "half-open intervals sharing an endpoint do not merge",
			in:   []Interval{iv(1, 2, true, false), iv(2, 3, true, false)},
			want: []Interval{iv(1, 2, true, false), iv(2, 3, true, false)},
		},
		{
			name: "open intervals sharing an endpoint do not merge",
			in:   []Interval{iv(1, 2, false, false), iv(2, 3, false, false)},
			want: []Interval{iv(1, 2, false, false), iv(2, 3, false, false)},
		},
		{
			name: "one inclusive side is enough to merge",
			in:   []Interval{iv(1, 2, true, true), iv(2, 3, true, false)},
			want: []Interval{iv(1, 3, true, false)},
		},
		{
			name: "overlapping intervals merge",
			in:   []Interval{iv(1, 3, true, true), iv(2, 4, true, true)},
			want: []Interval{iv(1, 4, true, true)},
		},
		{
			name: "a contained interval disappears",
			in:   []Interval{iv(1, 10, true, true), iv(3, 4, true, true)},
			want: []Interval{iv(1, 10, true, true)},
		},
		{
			name: "empty intervals are dropped",
			in:   []Interval{iv(5, 5, true, false), iv(9, 3, true, true), iv(1, 2, true, true)},
			want: []Interval{iv(1, 2, true, true)},
		},
		{
			name: "a single point is not empty when both endpoints are inclusive",
			in:   []Interval{iv(4, 4, true, true)},
			want: []Interval{iv(4, 4, true, true)},
		},
		{
			name: "output is sorted by start",
			in:   []Interval{iv(5, 6, true, true), iv(1, 2, true, true), iv(3, 4, true, true)},
			want: []Interval{iv(1, 2, true, true), iv(3, 4, true, true), iv(5, 6, true, true)},
		},
		{
			name: "equal starts are ordered by end",
			in:   []Interval{iv(1, 9, true, true), iv(1, 4, true, true)},
			want: []Interval{iv(1, 9, true, true)},
		},
		{
			name: "three intervals chain into one",
			in: []Interval{
				iv(1, 2, true, true), iv(2, 3, true, true), iv(3, 4, true, true),
			},
			want: []Interval{iv(1, 4, true, true)},
		},
		{
			name: "negative endpoints behave like positive ones",
			in:   []Interval{iv(-5, -2, true, true), iv(-2, 0, true, true)},
			want: []Interval{iv(-5, 0, true, true)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Merge(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("Merge(%v) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Merge(%v) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}

func TestMergeDoesNotMutateInput(t *testing.T) {
	in := []Interval{iv(5, 6, true, true), iv(1, 2, true, true)}
	before := append([]Interval(nil), in...)
	_ = Merge(in)
	if !reflect.DeepEqual(in, before) {
		t.Fatalf("Merge mutated its input: %v", in)
	}
}
