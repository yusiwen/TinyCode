package task

// Row is one student's scores.
type Row struct {
	Name   string
	Scores []int
}

// Average is the mean of the scores, or 0 when there are none.
func (r Row) Average() float64 {
	total := 0
	for _, score := range r.Scores {
		total += score
	}
	if len(r.Scores) == 0 {
		return 0
	}
	return float64(total) / float64(len(r.Scores))
}
