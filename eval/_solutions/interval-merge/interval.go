package task

import "sort"

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
	kept := make([]Interval, 0, len(in))
	for _, iv := range in {
		if iv.empty() {
			continue
		}
		kept = append(kept, iv)
	}

	sort.Slice(kept, func(i, j int) bool {
		if kept[i].Start != kept[j].Start {
			return kept[i].Start < kept[j].Start
		}
		if kept[i].End != kept[j].End {
			return kept[i].End < kept[j].End
		}
		return !kept[i].EndInclusive && kept[j].EndInclusive
	})

	out := make([]Interval, 0, len(kept))
	for _, iv := range kept {
		if len(out) == 0 {
			out = append(out, iv)
			continue
		}
		last := &out[len(out)-1]
		if merges(*last, iv) {
			*last = union(*last, iv)
			continue
		}
		out = append(out, iv)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// empty reports whether the interval contains no point.
func (iv Interval) empty() bool {
	if iv.Start > iv.End {
		return true
	}
	if iv.Start == iv.End {
		return !(iv.StartInclusive && iv.EndInclusive)
	}
	return false
}

// merges reports whether b can be folded into a, given a.Start <= b.Start.
func merges(a, b Interval) bool {
	if b.Start < a.End {
		return true
	}
	if b.Start > a.End {
		return false
	}
	// The endpoints coincide: the shared point has to belong to both.
	return a.EndInclusive && b.StartInclusive
}

// union folds b into a, keeping the wider endpoint. When the ends are equal the result is
// inclusive if either side was.
func union(a, b Interval) Interval {
	if b.End > a.End {
		return Interval{
			Start:          a.Start,
			End:            b.End,
			StartInclusive: a.StartInclusive,
			EndInclusive:   b.EndInclusive,
		}
	}
	if b.End < a.End {
		return Interval{
			Start:          a.Start,
			End:            a.End,
			StartInclusive: a.StartInclusive,
			EndInclusive:   a.EndInclusive,
		}
	}
	return Interval{
		Start:          a.Start,
		End:            a.End,
		StartInclusive: a.StartInclusive,
		EndInclusive:   a.EndInclusive || b.EndInclusive,
	}
}
