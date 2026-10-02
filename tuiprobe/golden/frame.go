// Package golden's frame helpers are for a caller that renders in-process — an
// application's own View() — rather than driving a program on a terminal. They are
// the same assertions the terminal path makes, applied to a string.
package golden

import (
	"fmt"
	"testing"
)

// Deterministic renders a frame twice at a geometry and requires the two to be
// byte-identical.
//
// A renderer that depends on map iteration order, on a cached style, or on the wall
// clock fails here instead of producing a golden that passes on one run and fails on
// the next. The harness learned this the expensive way: a flaky golden is worse than
// no golden, because it teaches everyone to re-run until it passes.
func Deterministic(cols, rows int, render func(cols, rows int) string) error {
	first := render(cols, rows)
	second := render(cols, rows)
	if first == second {
		return nil
	}
	return fmt.Errorf("rendering %dx%d twice produced different frames\n%s", cols, rows, Diff(first, second))
}

// AssertFrame is the in-process counterpart of a scenario's `golden` plus `fit`
// steps: the frame must fit the geometry it claims, and it must match the committed
// file (or rewrite it when Update() is set).
func AssertFrame(t testing.TB, dir, name string, cols, rows int, frame string) {
	t.Helper()
	if err := Fits(cols, rows, frame); err != nil {
		t.Errorf("%v", err)
	}
	Assert(t, dir, name, Normalize(frame))
}

// AssertDeterministic fails the test when a renderer is not repeatable, and returns
// the frame so the caller can assert on it too.
func AssertDeterministic(t testing.TB, cols, rows int, render func(cols, rows int) string) string {
	t.Helper()
	if err := Deterministic(cols, rows, render); err != nil {
		t.Errorf("%v", err)
	}
	return render(cols, rows)
}
