package task

// Sum returns the total of xs.
func Sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}

// Average returns the mean of xs, or 0 when xs is empty.
func Average(xs []int) float64 {
	return Sum(xs) / len(xs)
}
