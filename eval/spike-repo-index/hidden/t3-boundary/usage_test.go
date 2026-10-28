package task

import "testing"

func TestOffPeakIsHalfOpen(t *testing.T) {
	usage := NewUsage(0, 1, 2, 3, 4)
	if got := usage.OffPeak(1, 3); got != 2 {
		t.Errorf("OffPeak(1, 3) = %d, want 2: the window is [1, 3), so 1 and 2", got)
	}
	if got := usage.OffPeak(0, 0); got != 0 {
		t.Errorf("OffPeak(0, 0) = %d, want 0: an empty window", got)
	}
	if got := usage.OffPeak(4, 6); got != 1 {
		t.Errorf("OffPeak(4, 6) = %d, want 1", got)
	}
}

func TestPeakIsTheRest(t *testing.T) {
	usage := NewUsage(1, 2, 3)
	if got := usage.Peak(1, 3); got != 1 {
		t.Errorf("Peak(1, 3) = %d, want 1", got)
	}
}
