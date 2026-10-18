package task

import (
	"fmt"
	"strconv"
	"strings"
)

// Format renders a duration of ms milliseconds as <h>h<mm>m<ss>.<fff>s, with
// the hours unbounded.
func Format(ms int64) string {
	hours := ms / 3_600_000
	rest := ms % 3_600_000
	minutes := rest / 60_000
	rest %= 60_000
	seconds := rest / 1000
	millis := rest % 1000
	return fmt.Sprintf("%dh%02dm%02d.%03ds", hours, minutes, seconds, millis)
}

// Parse reads the shape Format writes.
func Parse(s string) (int64, error) {
	if len(s) < len("0h00m00.000s") {
		return 0, fmt.Errorf("parse duration %q: too short", s)
	}
	if !strings.HasSuffix(s, "s") {
		return 0, fmt.Errorf("parse duration %q: missing s", s)
	}
	// <h>h<mm>m<ss>.<fff>
	hIndex := strings.IndexByte(s, 'h')
	mIndex := strings.IndexByte(s, 'm')
	dot := strings.IndexByte(s, '.')
	if hIndex < 0 || mIndex < 0 || dot < 0 || !(hIndex < mIndex && mIndex < dot) {
		return 0, fmt.Errorf("parse duration %q: not <h>h<mm>m<ss>.<fff>s", s)
	}
	hours, err := strconv.ParseInt(s[:hIndex], 10, 64)
	if err != nil || hours < 0 {
		return 0, fmt.Errorf("parse duration %q: bad hours", s)
	}
	minuteField := s[hIndex+1 : mIndex]
	if len(minuteField) != 2 {
		return 0, fmt.Errorf("parse duration %q: minutes are not two digits", s)
	}
	minutes, err := strconv.ParseInt(minuteField, 10, 64)
	if err != nil || minutes > 59 {
		return 0, fmt.Errorf("parse duration %q: bad minutes", s)
	}
	secondField := s[mIndex+1 : dot]
	if len(secondField) != 2 {
		return 0, fmt.Errorf("parse duration %q: seconds are not two digits", s)
	}
	seconds, err := strconv.ParseInt(secondField, 10, 64)
	if err != nil || seconds > 59 {
		return 0, fmt.Errorf("parse duration %q: bad seconds", s)
	}
	fraction := s[dot+1 : len(s)-1]
	if len(fraction) != 3 {
		return 0, fmt.Errorf("parse duration %q: the fraction is not three digits", s)
	}
	millis, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: bad fraction", s)
	}
	return hours*3_600_000 + minutes*60_000 + seconds*1000 + millis, nil
}
