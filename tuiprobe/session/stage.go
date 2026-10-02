package session

import (
	"errors"
	"fmt"
	"time"
)

// ErrStageTimeout marks a stage that ran out of its budget, so a caller can tell
// "this step took too long" from "this step failed".
var ErrStageTimeout = errors.New("stage timed out")

// StageError names the stage that failed and how long it was given.
//
// A wedged step reported as "test failed" costs an afternoon; the same step
// reported as "stage wait-for-banner exceeded its 5s budget after 5.001s" is a
// diagnosis. That is the whole reason this type exists.
type StageError struct {
	Name    string
	Budget  time.Duration
	Elapsed time.Duration
	Timeout bool
	Err     error
}

func (e *StageError) Error() string {
	if e.Timeout {
		return fmt.Sprintf("stage %s exceeded its %s budget after %s", e.Name, e.Budget, e.Elapsed.Round(time.Millisecond))
	}
	return fmt.Sprintf("stage %s failed after %s: %v", e.Name, e.Elapsed.Round(time.Millisecond), e.Err)
}

func (e *StageError) Unwrap() error {
	if e.Timeout {
		return ErrStageTimeout
	}
	return e.Err
}

// Stage runs fn under a named, bounded budget.
//
// fn runs in its own goroutine so the budget is enforced even if fn ignores its
// own timeouts; a fn that never returns therefore leaks its goroutine, which is
// why the callers in this package all pass steps that respect the deadline they
// are given. The alternative — a step that can hang the tool with no attribution
// — is what this exists to prevent.
func Stage(name string, budget time.Duration, fn func() error) error {
	if budget <= 0 {
		budget = 10 * time.Second
	}
	done := make(chan error, 1)
	started := time.Now()
	go func() { done <- fn() }()

	select {
	case err := <-done:
		if err == nil {
			return nil
		}
		// A step that ran into its own deadline is a timeout, not a failure: the
		// caller's exit code must say "too slow", not "wrong".
		return &StageError{
			Name:    name,
			Budget:  budget,
			Elapsed: time.Since(started),
			Timeout: errors.Is(err, ErrStageTimeout),
			Err:     err,
		}
	case <-time.After(budget):
		return &StageError{Name: name, Budget: budget, Elapsed: time.Since(started), Timeout: true}
	}
}

// StageStable is the common stage shape: wait for the screen to settle.
func (s *Session) StageStable(name string, quiet, budget time.Duration) error {
	return Stage(name, budget, func() error { return s.WaitStable(quiet, budget) })
}

// StageText is the common stage shape: wait for the screen to match.
func (s *Session) StageText(name, pattern string, budget time.Duration) error {
	return Stage(name, budget, func() error { return s.WaitText(pattern, budget) })
}
