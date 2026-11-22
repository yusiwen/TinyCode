package task

// Interval is a range over int64. Each endpoint is inclusive or exclusive on its own.
type Interval struct {
	Start, End                   int64
	StartInclusive, EndInclusive bool
}

// Merge sorts and merges a list of intervals, returning a new slice and leaving the input
// untouched.
//
// Two intervals merge when they overlap, or when one ends exactly where the other begins
// **and that shared point belongs to both**:
//
//	[1,2] + [2,3]  ->  [1,3]        2 is in both
//	[1,2) + [2,3]  ->  unchanged    2 is in neither
//	(1,2) + (2,3)  ->  unchanged
//	[1,2) + (2,3]  ->  unchanged
//	[1,3] + [2,4]  ->  [1,4]        they overlap
//
// The merged interval keeps the wider endpoint, and an endpoint at the merge point is
// inclusive only if at least one input made it inclusive. Nothing is ever widened beyond
// the inputs: merging [1,2) with [2,3) is a no-op, not [1,3).
//
// Empty intervals are dropped, where empty means Start > End, or Start == End with either
// endpoint excluded. The result is sorted by Start, and intervals sharing a Start are
// ordered by End, so the output is deterministic for a given input.
func Merge(in []Interval) []Interval {
	return nil
}
