package task

// CountInversions returns the number of pairs i < j with xs[i] > xs[j].
//
// One of the tests calls this on a large slice and fails the suite when it takes
// too long, so the work is done by a counting merge sort: O(n log n), with the
// cross-half inversions counted while the halves are merged.
func CountInversions(xs []int) int64 {
	work := make([]int, len(xs))
	copy(work, xs)
	count := int64(0)
	sortAndCount(work, make([]int, len(xs)), &count)
	return count
}

// sortAndCount sorts xs in place and adds the inversions it finds to count.
func sortAndCount(xs, buffer []int, count *int64) {
	if len(xs) < 2 {
		return
	}
	mid := len(xs) / 2
	sortAndCount(xs[:mid], buffer[:mid], count)
	sortAndCount(xs[mid:], buffer[mid:], count)

	// Merge, counting: every time an element of the right half is taken before
	// the remaining left half, it is smaller than all of them.
	i, j, k := 0, mid, 0
	for i < mid && j < len(xs) {
		if xs[i] <= xs[j] {
			buffer[k] = xs[i]
			i++
		} else {
			buffer[k] = xs[j]
			j++
			*count += int64(mid - i)
		}
		k++
	}
	for i < mid {
		buffer[k] = xs[i]
		i++
		k++
	}
	for j < len(xs) {
		buffer[k] = xs[j]
		j++
		k++
	}
	copy(xs, buffer[:k])
}
