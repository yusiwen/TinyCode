package task

import "testing"

func TestFormat(t *testing.T) {
	cases := map[int64]string{
		0:          "0h00m00.000s",
		400:        "0h00m00.400s",
		61_500:     "0h01m01.500s",
		3_600_000:  "1h00m00.000s",
		3_723_456:  "1h02m03.456s",
		90_000_000: "25h00m00.000s",
	}
	for ms, want := range cases {
		if got := Format(ms); got != want {
			t.Errorf("Format(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	for _, ms := range []int64{0, 1, 999, 1000, 61_500, 3_723_456, 90_000_000} {
		got, err := Parse(Format(ms))
		if err != nil {
			t.Errorf("Parse(Format(%d)) = error %v", ms, err)
			continue
		}
		if got != ms {
			t.Errorf("Parse(Format(%d)) = %d", ms, got)
		}
	}
}

func TestParseRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "1h2m", "1h02m03.456", "0h00m00.4s", "zz h00m00.000s"} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) = nil error, want one", s)
		}
	}
}
