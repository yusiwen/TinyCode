// Package clock is the indirection that lets a test decide what time it is.
package clock

import "time"

// Clock reports the current time.
type Clock interface {
	Now() time.Time
}

// Real reads the wall clock.
type Real struct{}

// Now returns time.Now.
func (Real) Now() time.Time { return time.Now() }

// Fake returns a fixed time until Set moves it.
type Fake struct{ at time.Time }

// NewFake starts a fake clock at t.
func NewFake(t time.Time) *Fake { return &Fake{at: t} }

// Now returns the fake's current time.
func (f *Fake) Now() time.Time { return f.at }

// Set moves the fake clock.
func (f *Fake) Set(t time.Time) { f.at = t }

// Advance moves the fake clock forward by d.
func (f *Fake) Advance(d time.Duration) { f.at = f.at.Add(d) }
