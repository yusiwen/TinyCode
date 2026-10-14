package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// The input area — the permission dialog, the provider picker, the command
// palette and the key hints under them — is drawn outside the cell grid, so
// nothing else trims it against the terminal. A line wider than the terminal is
// cut by the terminal itself, mid-word and without a mark (issue #218).

// TestPermissionDialogFitsFortyColumns is issue #218's first acceptance clause, at
// the level a user meets it: the real model's permission dialog rendered at 40
// columns has no line wider than 40 cells. Before the fix its hint row was 54 and
// its heading 47.
func TestPermissionDialogFitsFortyColumns(t *testing.T) {
	var frame string
	withTrueColor(t, func() { frame = frameDialog(40, 12).View() })

	if widest, line := widestCellLine(frame); widest > 40 {
		t.Errorf("the dialog's widest line is %d cells, want at most 40: %q", widest, line)
	}
}

// TestPermissionDialogKeepsEveryHintAndWord is the acceptance's second clause: what
// does not fit is laid out again, never dropped silently. `Esc cancel` is the only
// way out of the dialog and `1-9 shortcut` is how the keyboard answers it, so both
// have to survive 40 columns in full.
func TestPermissionDialogKeepsEveryHintAndWord(t *testing.T) {
	var frame string
	withTrueColor(t, func() { frame = frameDialog(40, 12).View() })
	plain := stripANSI(frame)

	for _, want := range []string{
		"↑↓ navigate", "Enter select", "1-9 shortcut", "Esc cancel",
		"Allow write outside the project root?", "../build",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("the dialog at 40 columns lost %q:\n%s", want, plain)
		}
	}
}

// TestHintRowsKeepEveryAlternativeAtAnyWidth drives the three hint rows the input
// area can show: whatever the width, each line fits it and no alternative is lost
// to the layout.
func TestHintRowsKeepEveryAlternativeAtAnyWidth(t *testing.T) {
	rows := map[string][]string{
		"dialog":   dialogHintAlternatives,
		"provider": providerHintAlternatives,
		"palette":  paletteHintAlternatives,
	}

	for name, alternatives := range rows {
		for _, width := range []int{120, 40, 30, 20} {
			lines := fitHintRow(alternatives, width)
			joined := strings.Join(lines, " ")
			for _, line := range lines {
				if got := lipgloss.Width(line); got > width {
					t.Errorf("%s at %d columns: a %d-cell line would be cut by the terminal: %q",
						name, width, got, line)
				}
			}
			for _, alt := range alternatives {
				if !strings.Contains(joined, alt) {
					t.Errorf("%s at %d columns lost %q: %q", name, width, alt, joined)
				}
			}
		}
	}
}

// TestHintRowWrapsAtWholeAlternatives pins the shape at the width the issue was
// measured at: 40 columns puts the dialog's row on two lines, in order, with the
// separator trailing the wrapped line so it still reads as one list.
func TestHintRowWrapsAtWholeAlternatives(t *testing.T) {
	got := fitHintRow(dialogHintAlternatives, 40)
	want := []string{
		"↑↓ navigate · Enter select ·",
		"1-9 shortcut · Esc cancel",
	}
	if len(got) != len(want) {
		t.Fatalf("at 40 columns the row wrapped into %d lines, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestHintRowClipsAnAlternativeWiderThanTheLine covers the case wrapping cannot
// solve: an alternative that does not fit a whole line by itself is clipped with a
// visible marker, because an unmarked cut reads as a shorter alternative.
func TestHintRowClipsAnAlternativeWiderThanTheLine(t *testing.T) {
	const width = 6
	got := fitHintRow([]string{"↑↓ navigate", "Enter select"}, width)

	if len(got) == 0 {
		t.Fatal("no lines at all")
	}
	for _, line := range got {
		if w := lipgloss.Width(line); w > width {
			t.Errorf("a %d-cell line would be cut by the terminal: %q", w, line)
		}
	}
	if !strings.HasSuffix(got[0], "…") {
		t.Errorf("the clipped alternative is not marked: %q", got)
	}
}

// TestInputTextWrapsAtWordsAndLosesNothing: the sandbox's permission title is a
// sentence around a path, and a path cut in half is worse than a shorter sentence.
// Every word survives, and each line fits the room the style and the marker leave.
func TestInputTextWrapsAtWordsAndLosesNothing(t *testing.T) {
	const text = "Allow write outside the project root? ../build"

	for _, indent := range []int{0, 2, 6} {
		lines := wrapInputText(text, 40, indent, indent)
		limit := 40 - indent
		for _, line := range lines {
			if w := lipgloss.Width(line); w > limit {
				t.Errorf("indent %d: a %d-cell line exceeds the %d cells left for it: %q",
					indent, w, limit, line)
			}
		}
		if got := strings.Join(lines, " "); got != text {
			t.Errorf("indent %d: wrapping lost or reordered words:\n got %q\nwant %q", indent, got, text)
		}
	}
}

// TestInputTextSplitsAWordWiderThanTheLine: a long path with no space in it cannot be
// wrapped at a word boundary, so it is split across lines. Clipping it would hide the
// file the answer writes to, which is the one thing that label exists to show.
func TestInputTextSplitsAWordWiderThanTheLine(t *testing.T) {
	const text = "/Users/someone/with/a/very/long/path/that/does/not/fit/anywhere/config.json"
	lines := wrapInputText(text, 40, 0, 0)

	if len(lines) < 2 {
		t.Fatalf("a word wider than the line was not split: %q", lines)
	}
	for _, line := range lines {
		if w := lipgloss.Width(line); w > 40 {
			t.Errorf("a %d-cell line would be cut by the terminal: %q", w, line)
		}
	}
	if got := strings.Join(lines, ""); got != text {
		t.Errorf("splitting lost or reordered characters:\n got %q\nwant %q", got, text)
	}
	if strings.Contains(strings.Join(lines, ""), "…") {
		t.Errorf("a split word must not be marked as clipped: %q", lines)
	}
}

// TestWrappedInputTextUsesTheWiderContinuationLine pins the shape the always-allow
// answer takes at 40 columns: the first line carries the `[3] ` marker and wraps
// early, while the path continues under it in the wider column the marker leaves.
func TestWrappedInputTextUsesTheWiderContinuationLine(t *testing.T) {
	const item = "Always allow (saves to /Users/yusiwen/.tinycode/config.json)"
	got := wrapInputText(item, 40, 6, 2)

	want := []string{
		"Always allow (saves to",
		"/Users/yusiwen/.tinycode/config.json)",
	}
	if len(got) != len(want) {
		t.Fatalf("wrapped into %d lines, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestInputAreaWithoutAKnownWidthIsUnchanged keeps the "the terminal has not told us
// its size yet" path as it was: nothing is wrapped or dropped on a guess.
func TestInputAreaWithoutAKnownWidthIsUnchanged(t *testing.T) {
	const heading = "Allow write outside the project root? ../build"
	if got := wrapInputText(heading, 0, 2, 2); len(got) != 1 || got[0] != heading {
		t.Errorf("width 0 = %q, want the heading unchanged on one line", got)
	}
	if got := fitHintRow(dialogHintAlternatives, 0); len(got) != 1 ||
		got[0] != strings.Join(dialogHintAlternatives, hintSeparator) {
		t.Errorf("width 0 = %q, want the whole row on one line", got)
	}
}

// TestPermissionDialogTakesTheRealAnswersAtFortyColumns drives the model with the
// dialog the sandbox actually raises: the four answers, including the one that names
// the config file it writes, and a title around a real path. The always-allow label
// is longer than 40 cells on its own, and the title is longer once its style padding
// is added, so this is where "every line fits" is worth the most.
func TestPermissionDialogTakesTheRealAnswersAtFortyColumns(t *testing.T) {
	m := frameDialog(40, 12)
	m.dialogMsg = "🔒 Write to /tmp/stub-outside-write.txt?"
	m.dialogItems = []string{
		allowOnceLabel,
		allowSessionLabel,
		"Always allow (saves to /Users/someone/.tinycode/config.json)",
		"Deny",
	}

	var frame string
	withTrueColor(t, func() { frame = m.View() })
	if widest, line := widestCellLine(frame); widest > 40 {
		t.Errorf("the real dialog's widest line is %d cells, want at most 40: %q", widest, line)
	}
	// The label is wrapped, not truncated: the whole path is still on screen.
	if !strings.Contains(stripANSI(frame), "config.json)") {
		t.Errorf("the always-allow answer lost its target file:\n%s", stripANSI(frame))
	}
}

// TestPaletteRowsFitANarrowTerminal is issue #221's first acceptance clause: at 30
// columns — narrower than the widest command row, 36 cells — every line of the
// palette fits, and below that too. Nothing is lost on the way: each command's
// description is still on screen in full, and the test at 16 columns is a
// description longer than the terminal.
func TestPaletteRowsFitANarrowTerminal(t *testing.T) {
	for _, width := range []int{30, 20, 16} {
		var frame string
		withTrueColor(t, func() { frame = framePalette(width, 12).View() })

		if widest, line := widestCellLine(frame); widest > width {
			t.Errorf("at %d columns the palette's widest line is %d cells: %q", width, widest, line)
		}
		// Whitespace is removed before the comparison, so a description that was
		// wrapped or split across lines still counts as present — and one that was
		// clipped does not.
		packed := strings.Join(strings.Fields(stripANSI(frame)), "")
		for _, c := range commandList() {
			if want := strings.ReplaceAll(c.Desc, " ", ""); !strings.Contains(packed, want) {
				t.Errorf("at %d columns %s lost its description %q", width, c.Name, c.Desc)
			}
		}
	}
}

// TestPaletteRowsKeepTheDescriptionWhole: a description that fits beside its name
// sits there, and one that does not takes a line of its own rather than being split
// into the narrow column beside the name — `LSP` / `diagnostics` on the row above is
// what that looks like, and it is not a description (issue #221).
func TestPaletteRowsKeepTheDescriptionWhole(t *testing.T) {
	var frame string
	withTrueColor(t, func() { frame = framePalette(30, 12).View() })

	for _, want := range []string{
		"  /plan  Plan mode",                  // fits beside the name, so it stays there
		"  /diagnostics\n    LSP diagnostics", // does not fit, so it moves below
		"  /thinking\n    Toggle thinking display",
	} {
		if !strings.Contains(stripANSI(frame), want) {
			t.Errorf("the palette does not contain %q:\n%s", want, stripANSI(frame))
		}
	}
}

// TestProviderRowsFitANarrowTerminal is the picker's half of the acceptance: its
// labels come from the user's own provider names, so one here is longer than the
// terminal it is drawn in. The label is wrapped, never cut, and the row marker stays
// on the first line.
func TestProviderRowsFitANarrowTerminal(t *testing.T) {
	m := frameProvider(0, 12)
	records := m.provReg.List()

	for _, width := range []int{80, 30, 16} {
		var frame string
		withTrueColor(t, func() { frame = frameProvider(width, 12).View() })

		if widest, line := widestCellLine(frame); widest > width {
			t.Errorf("at %d columns the picker's widest line is %d cells: %q", width, widest, line)
		}
		packed := strings.Join(strings.Fields(stripANSI(frame)), "")
		for _, rec := range records {
			label := fmt.Sprintf("%s (%s)", rec.Name, rec.Provider.Name())
			if want := strings.ReplaceAll(label, " ", ""); !strings.Contains(packed, want) {
				t.Errorf("at %d columns the picker lost the label %q", width, label)
			}
		}
	}

	// The marker belongs to the row: a wrapped label continues under its own text,
	// not under a second `>`.
	plain := stripANSI(mustView(t, frameProvider(30, 12)))
	if got := strings.Count(plain, ">"); got != 1 {
		t.Errorf("the picker drew %d row markers, want 1 on the selected row:\n%s", got, plain)
	}
}

// TestSplitAtCellsPrefersABreakPoint: a long token with no space in it is split
// where a reader expects — after a hyphen or a slash — and at the cell limit only
// when it has neither. `openrouter-anthropic-claud` / `e-sonnet` is what the cell
// limit alone produces.
func TestSplitAtCellsPrefersABreakPoint(t *testing.T) {
	for _, tc := range []struct {
		word     string
		limit    int
		wantHead string
		wantTail string
	}{
		{"openrouter-anthropic-claude-sonnet", 26, "openrouter-anthropic-", "claude-sonnet"},
		{"/Users/someone/.tinycode/config.json", 26, "/Users/someone/.tinycode/", "config.json"},
		{"abcdefgh", 3, "abc", "defgh"},
		{"-abc", 1, "-", "abc"},
		{"日本語", 4, "日本", "語"},
	} {
		head, tail := splitAtCells(tc.word, tc.limit)
		if head != tc.wantHead || tail != tc.wantTail {
			t.Errorf("splitAtCells(%q, %d) = %q, %q; want %q, %q",
				tc.word, tc.limit, head, tail, tc.wantHead, tc.wantTail)
		}
		if head == "" || tail == "" {
			t.Errorf("splitAtCells(%q, %d) did not make progress", tc.word, tc.limit)
		}
		if w := lipgloss.Width(head); w > tc.limit {
			t.Errorf("splitAtCells(%q, %d) returned a %d-cell head", tc.word, tc.limit, w)
		}
	}
}

// mustView renders a model with the colour profile pinned.
func mustView(t *testing.T, m *TuiModel) string {
	t.Helper()
	var frame string
	withTrueColor(t, func() { frame = m.View() })
	return frame
}

// widestCellLine returns the width of the widest line of a rendered frame, in cells,
// and the line itself so a failure can show what overran.
func widestCellLine(frame string) (int, string) {
	widest, widestLine := 0, ""
	for _, line := range strings.Split(frame, "\n") {
		if w := lipgloss.Width(line); w > widest {
			widest, widestLine = w, line
		}
	}
	return widest, stripANSI(widestLine)
}
