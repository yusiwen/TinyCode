package task

// Last returns the last element of xs, or 0 when xs is empty.
func Last(xs []int) int {
	if len(xs) == 0 {
		return 0
	}
	return xs[len(xs)-1]
}
