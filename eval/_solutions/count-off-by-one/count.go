package task

// Count returns how many elements of xs are at least threshold.
func Count(xs []int, threshold int) int {
	count := 0
	for i := 0; i < len(xs); i++ {
		if xs[i] >= threshold {
			count++
		}
	}
	return count
}
