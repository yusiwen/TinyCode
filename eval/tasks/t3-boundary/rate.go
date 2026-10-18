package task

// Usage holds the ticks a route was asked during.
type Usage struct {
	ticks []int
}

// NewUsage returns a usage record over the given ticks.
func NewUsage(ticks ...int) *Usage { return &Usage{ticks: ticks} }

// OffPeak counts the ticks inside [from, to).
func (u *Usage) OffPeak(from, to int) int {
	count := 0
	for _, tick := range u.ticks {
		if InWindow(tick, from, to) {
			count++
		}
	}
	return count
}

// Peak counts the ticks outside [from, to).
func (u *Usage) Peak(from, to int) int { return len(u.ticks) - u.OffPeak(from, to) }
