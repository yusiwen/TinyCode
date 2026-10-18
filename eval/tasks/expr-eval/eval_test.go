package task

import "testing"

func TestEvaluate(t *testing.T) {
	cases := map[string]int{
		"1+2*3":       7,
		"(1+2)*3":     9,
		"2*(3+4)-5":   9,
		"10/3":        3,
		"-10/3":       -3,
		"-5+2":        -3,
		"2*-3":        -6,
		"1+2+3":       6,
		"10-2-3":      5,
		"100/5/2":     10,
		"((1))":       1,
		" 1 + 2 ":     3,
		"7":           7,
		"3*(2+1)*(4)": 36,
	}
	for expr, want := range cases {
		got, err := Evaluate(expr)
		if err != nil {
			t.Errorf("Evaluate(%q) = error %v", expr, err)
			continue
		}
		if got != want {
			t.Errorf("Evaluate(%q) = %d, want %d", expr, got, want)
		}
	}
}

func TestEvaluateRejectsMalformed(t *testing.T) {
	for _, expr := range []string{"", "1+", "(1", "1)", "1/0", "+", "1 2", "()", "3*/2"} {
		if _, err := Evaluate(expr); err == nil {
			t.Errorf("Evaluate(%q) = nil error, want one", expr)
		}
	}
}
