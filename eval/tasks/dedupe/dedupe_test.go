package task

import (
	"reflect"
	"testing"
)

func TestDedupe(t *testing.T) {
	cases := []struct {
		in   []string
		want []string
	}{
		{[]string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
		{[]string{}, []string{}},
		{[]string{"x"}, []string{"x"}},
	}
	for _, tc := range cases {
		if got := Dedupe(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Dedupe(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
