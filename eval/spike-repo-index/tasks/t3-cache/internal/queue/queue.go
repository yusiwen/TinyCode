// Package queue is a fixed-capacity ring buffer of strings.
package queue

// Ring holds at most cap items; the oldest is overwritten when full.
type Ring struct {
	items []string
	head  int
	size  int
}

// New returns a ring of the given capacity.
func New(capacity int) *Ring { return &Ring{items: make([]string, capacity)} }

// Push appends one item, overwriting the oldest when the ring is full.
func (r *Ring) Push(item string) {
	if len(r.items) == 0 {
		return
	}
	r.items[r.head] = item
	r.head = (r.head + 1) % len(r.items)
	if r.size < len(r.items) {
		r.size++
	}
}

// Len reports how many items the ring holds.
func (r *Ring) Len() int { return r.size }

// Slice returns the items oldest first.
func (r *Ring) Slice() []string {
	out := make([]string, 0, r.size)
	start := (r.head - r.size + len(r.items)) % len(r.items)
	for i := 0; i < r.size; i++ {
		out = append(out, r.items[(start+i)%len(r.items)])
	}
	return out
}
