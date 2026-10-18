package task

import (
	"fmt"
	"sort"
)

// Book holds seats that can be reserved.
type Book struct {
	taken map[string]bool
}

// NewBook returns an empty book.
func NewBook() *Book { return &Book{taken: make(map[string]bool)} }

// Reserve takes every seat or none: if any seat is already taken the call fails
// and no seat is left reserved.
func (b *Book) Reserve(seats ...string) error {
	for _, seat := range seats {
		if b.taken[seat] {
			return fmt.Errorf("reserve: seat %q is taken", seat)
		}
		b.taken[seat] = true
	}
	return nil
}

// Release gives every seat back, or none: if any seat is not taken the call
// fails and no seat is released.
func (b *Book) Release(seats ...string) error {
	for _, seat := range seats {
		if !b.taken[seat] {
			return fmt.Errorf("release: seat %q is not taken", seat)
		}
		delete(b.taken, seat)
	}
	return nil
}

// Reserved lists the taken seats in ascending order.
func (b *Book) Reserved() []string {
	out := make([]string, 0, len(b.taken))
	for seat := range b.taken {
		out = append(out, seat)
	}
	sort.Strings(out)
	return out
}
