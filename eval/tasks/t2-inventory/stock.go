package task

// Stock tracks how many units are on hand and how many have been reserved.
type Stock struct {
	onHand   int
	reserved int
}

// NewStock returns a stock with the given number of units on hand.
func NewStock(onHand int) *Stock { return &Stock{onHand: onHand} }

// Reserve sets aside n units. It refuses more than is available, and a refused
// reservation changes nothing.
func (s *Stock) Reserve(n int) error {
	s.reserved += n
	return nil
}

// Available is how many units can still be reserved.
func (s *Stock) Available() int { return s.onHand }
