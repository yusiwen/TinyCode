package task

// Clamp returns v limited to the range [lo, hi]. It assumes lo <= hi.
func Clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
