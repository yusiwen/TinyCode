package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestStatusBarKeepsTheMessageWhole is issue #196's first acceptance clause: with a
// budget, a charge and a message all present, the message's full text is on screen
// at 80 and at 120 columns.
func TestStatusBarKeepsTheMessageWhole(t *testing.T) {
	message := "  │ Press Ctrl+C again to quit"
	segments := []string{
		"⚡ plan",
		"  ■ openai/stub",
		"  tokens: 1500/100000",
		"  cost: 0.0042 credits",
		"  tools: 0",
		"  msgs: 3",
		"  session: 15s",
	}

	for _, width := range []int{80, 120, 200} {
		got := fitStatusBar(segments, message, width)
		if !strings.Contains(got, message) {
			t.Errorf("at %d columns the message is not whole: %q", width, got)
		}
		if w := lipgloss.Width(got); w > width {
			t.Errorf("at %d columns the bar is %d cells wide, which the terminal would clip", width, w)
		}
	}
}

// TestStatusBarDropsWholeSegmentsOnly is the acceptance's third clause: a segment
// cut in half can be misread (`0.95 credits` as `0.95 credit`, a fraction losing
// its limit), so a segment that does not fit is left out entirely.
func TestStatusBarDropsWholeSegmentsOnly(t *testing.T) {
	message := "  │ Press Ctrl+C again to quit"
	segments := []string{
		"⚡ plan",
		"  ■ openai/stub",
		"  tokens: 1500/100000",
		"  cost: 0.0042 credits",
		"  tools: 0",
		"  msgs: 3",
		"  session: 15s",
	}

	got := fitStatusBar(segments, message, 60)
	if !strings.Contains(got, message) {
		t.Fatalf("the message did not survive 60 columns: %q", got)
	}
	// Every segment is either present in full or absent; none is a prefix.
	for _, segment := range segments {
		if present := strings.Contains(got, segment); present {
			continue
		}
		// Absent: assert no partial tail of it leaked in. The last few cells are the
		// ones a half-cut would leave behind.
		for _, probe := range partialProbes(segment) {
			if strings.Contains(got, probe) {
				t.Errorf("segment %q was cut rather than dropped: %q contains %q", segment, got, probe)
			}
		}
	}
}

// partialProbes returns the substrings a half-cut segment would leave: its own
// content without its leading spaces and without its last rune.
func partialProbes(segment string) []string {
	trimmed := strings.TrimLeft(segment, " ")
	if len([]rune(trimmed)) < 4 {
		return nil
	}
	runes := []rune(trimmed)
	return []string{string(runes[:len(runes)-1]), string(runes[:len(runes)-2])}
}

// TestStatusBarClipsTheMessageVisibly: when nothing can make the message fit, it is
// clipped with a marker, because without one a clipped message reads as a shorter
// one.
func TestStatusBarClipsTheMessageVisibly(t *testing.T) {
	message := "  │ Press Ctrl+C again to quit"
	got := fitStatusBar([]string{"⚡ plan", "  ■ openai/stub"}, message, 20)

	if !strings.HasSuffix(got, "…") {
		t.Errorf("a clipped message must be marked: %q", got)
	}
	if w := lipgloss.Width(got); w > 20 {
		t.Errorf("clipped bar is %d cells wide, want at most 20", w)
	}
	if strings.Contains(got, "quit") {
		t.Errorf("the marker is missing though the text looks complete: %q", got)
	}
}

// TestStatusBarWithoutAKnownWidthIsUnchanged keeps the path for "the terminal has
// not told us its size yet" as it was: nothing is dropped on a guess.
func TestStatusBarWithoutAKnownWidthIsUnchanged(t *testing.T) {
	segments := []string{"⚡ plan", "  ■ openai/stub", "  session: 15s"}
	message := "  │ Press Ctrl+C again to quit"

	got := fitStatusBar(segments, message, 0)
	if got != strings.Join(segments, "")+message {
		t.Errorf("width 0 = %q, want the whole bar", got)
	}
}

// TestRenderedBarKeepsItsMessageAtEightyColumns is the acceptance's other width, at
// the level a user meets it: the real model's bar — a budget fraction, a charge and a
// status message all present — rendered at 80 columns. The tests above drive
// fitStatusBar with a hand-built segment list; this one drives the model.
func TestRenderedBarKeepsItsMessageAtEightyColumns(t *testing.T) {
	const message = "Press Ctrl+C again to quit"

	for _, width := range []int{80, 120} {
		m := frameStatusBar(width, 24)
		m.statusMsg = message

		bar := m.renderStatusBar()
		if !strings.Contains(bar, message) {
			t.Errorf("at %d columns the message is not whole:\n%s", width, bar)
		}
		if got := lipgloss.Width(bar); got > width {
			t.Errorf("at %d columns the bar is %d cells wide, which the terminal would clip", width, got)
		}
	}
}
