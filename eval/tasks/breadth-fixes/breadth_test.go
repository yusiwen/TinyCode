package task

import (
	"reflect"
	"testing"
)

func TestTitle(t *testing.T) {
	cases := map[string]string{
		"hello world": "Hello World",
		"":            "",
		"a":           "A",
		"two  words":  "Two  Words",
	}
	for in, want := range cases {
		if got := Title(in); got != want {
			t.Errorf("Title(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClamp(t *testing.T) {
	cases := []struct{ v, lo, hi, want int }{
		{5, 0, 10, 5},
		{-5, 0, 10, 0},
		{50, 0, 10, 10},
		{0, 0, 0, 0},
	}
	for _, tc := range cases {
		if got := Clamp(tc.v, tc.lo, tc.hi); got != tc.want {
			t.Errorf("Clamp(%d, %d, %d) = %d, want %d", tc.v, tc.lo, tc.hi, got, tc.want)
		}
	}
}

func TestLast(t *testing.T) {
	if got := Last([]int{1, 2, 3}); got != 3 {
		t.Errorf("Last([1 2 3]) = %d, want 3", got)
	}
	if got := Last(nil); got != 0 {
		t.Errorf("Last(nil) = %d, want 0", got)
	}
}

func TestSortedKeys(t *testing.T) {
	got := SortedKeys(map[string]int{"b": 2, "a": 1, "c": 3})
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SortedKeys = %v, want %v", got, want)
	}
}

func TestPad(t *testing.T) {
	if got := Pad("ab", 5); got != "ab   " {
		t.Errorf("Pad(\"ab\", 5) = %q, want %q", got, "ab   ")
	}
	if got := Pad("abcdef", 3); got != "abcdef" {
		t.Errorf("Pad(\"abcdef\", 3) = %q, want it unchanged", got)
	}
}
