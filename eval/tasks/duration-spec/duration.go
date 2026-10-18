package task

// Format renders a duration of ms milliseconds in exactly this shape:
//
//	<h>h<mm>m<ss>.<fff>s
//
// where the hours are unbounded and printed without padding, the minutes and
// seconds are two-digit fields, and the fraction is always three digits. So
// zero is "0h00m00.000s", 3723456 is "1h02m03.456s", and 61500 is
// "0h01m01.500s".
func Format(ms int64) string {
	return ""
}

// Parse reads the shape Format writes and returns the milliseconds it holds.
func Parse(s string) (int64, error) {
	return 0, nil
}
