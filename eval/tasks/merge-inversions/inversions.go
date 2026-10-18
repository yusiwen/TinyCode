package task

// CountInversions returns the number of pairs i < j with xs[i] > xs[j].
//
// One of the tests calls this on a large slice and fails the suite when it takes
// too long, so the double loop below is the reference for the answer and not for
// the implementation.
func CountInversions(xs []int) int64 {
	var count int64
	for i := 0; i < len(xs); i++ {
		for j := i + 1; j < len(xs); j++ {
			if xs[i] > xs[j] {
				count++
			}
		}
	}
	return count
}
