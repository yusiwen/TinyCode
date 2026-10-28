// Package retry computes the wait between attempts.
package retry

import (
	"math"
	"time"
)

// Policy is a capped exponential backoff.
type Policy struct {
	Base  time.Duration
	Max   time.Duration
	Tries int
}

// Wait returns how long to sleep before attempt n (1-based).
func (p Policy) Wait(n int) time.Duration {
	if n <= 1 {
		return 0
	}
	wait := time.Duration(float64(p.Base) * math.Pow(2, float64(n-2)))
	if wait > p.Max || wait <= 0 {
		return p.Max
	}
	return wait
}

// Exhausted reports whether n attempts have been spent.
func (p Policy) Exhausted(n int) bool { return p.Tries > 0 && n >= p.Tries }
