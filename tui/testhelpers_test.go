package tui

// Test helpers that belong to the project rather than to visual verification: the
// geometry test reports its diffs with frameDiff, failure messages shorten output with
// tail, and the golden tests pin the colour profile. Everything the visual harness used
// to provide now comes from tuiprobe — see verification_test.go.

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/yusiwen/TinyCode/tuiprobe/golden"
)

// frameDiff reports the first differing line, which is what a reader needs. The
// comparison itself lives in tuiprobe/golden, so a golden failure and a geometry-test
// failure explain themselves the same way.
func frameDiff(want, got string) string {
	return golden.Diff(want, got)
}

// tail returns the last n bytes of s.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// withTrueColor pins the lipgloss profile to TrueColor and empties the style cache for
// the duration of a render. Without it a non-TTY stdout renders pure ASCII and every
// style vanishes, which would turn every frame assertion into a test of plain text.
func withTrueColor(t *testing.T, fn func()) {
	t.Helper()
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	resetStyleCache()
	defer func() {
		lipgloss.SetColorProfile(previous)
		resetStyleCache()
	}()
	fn()
}

func resetStyleCache() {
	styleMu.Lock()
	styleCache = map[CellStyle]lipgloss.Style{}
	styleMu.Unlock()
}
