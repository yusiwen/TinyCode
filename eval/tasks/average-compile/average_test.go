package task

import "testing"

func TestAverage(t *testing.T) {
	if got := Average([]int{1, 2, 3}); got != 2 {
		t.Errorf("Average([1 2 3]) = %v, want 2", got)
	}
	if got := Average(nil); got != 0 {
		t.Errorf("Average(nil) = %v, want 0", got)
	}
	if got := Average([]int{1, 2}); got != 1.5 {
		t.Errorf("Average([1 2]) = %v, want 1.5", got)
	}
}
