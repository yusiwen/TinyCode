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

// Reserve takes every seat or none: it checks the whole request before it
// changes anything, so a refusal leaves the book as it was.
func (b *Book) Reserve(seats ...string) error {
	for _, seat := range seats {
		if b.taken[seat] {
			return fmt.Errorf("reserve: seat %q is taken", seat)
		}
	}
	for _, seat := range seats {
		b.taken[seat] = true
	}
	return nil
}

// Release gives every seat back, or none: it checks the whole request first, so
// a refusal leaves the book as it was.
func (b *Book) Release(seats ...string) error {
	for _, seat := range seats {
		if !b.taken[seat] {
			return fmt.Errorf("release: seat %q is not taken", seat)
		}
	}
	for _, seat := range seats {
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
