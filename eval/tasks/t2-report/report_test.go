package task

import "testing"

func TestReport(t *testing.T) {
	rows := []Row{
		{Name: "nina", Scores: []int{3, 4}},
		{Name: "amir", Scores: []int{5, 5, 5}},
		{Name: "zoe"},
	}
	want := "amir: 5.0\nnina: 3.5\nzoe: 0.0\n"
	if got := Report(rows); got != want {
		t.Errorf("Report =\n%q\nwant\n%q", got, want)
	}
}

func TestReportOfNothingIsEmpty(t *testing.T) {
	if got := Report(nil); got != "" {
		t.Errorf("Report(nil) = %q, want empty", got)
	}
}
