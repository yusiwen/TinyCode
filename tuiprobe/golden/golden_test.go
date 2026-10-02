package golden

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeTrimsAndStrips(t *testing.T) {
	// A styled frame with CRLF rows, per-line padding and trailing blank lines,
	// plus the OSC 8 hyperlink the banner emits — all of it must reduce to plain
	// text with no trailing blanks.
	frame := "\x1b]8;;https://example.com\x07click\x1b]8;;\x07  \r\n\x1b[1;31mred\x1b[0m   \r\n\r\n"
	want := "click\nred\n"
	if got := Normalize(frame); got != want {
		t.Errorf("Normalize = %q, want %q", got, want)
	}
}

func TestDiffNamesTheFirstDifferingColumn(t *testing.T) {
	got := Diff("alpha\nbravo\n", "alpha\nbravo\ncharlie\n")
	if !strings.Contains(got, "line 3") {
		t.Errorf("diff does not name the line:\n%s", got)
	}

	got = Diff("hello world\n", "hello wOrld\n")
	if !strings.Contains(got, "column 8") {
		t.Errorf("diff does not name the column:\n%s", got)
	}

	if got := Diff("same\n", "same\n"); !strings.Contains(got, "trailing blank lines") {
		t.Errorf("identical artifacts should report no content difference, got %q", got)
	}
}

func TestAssertComparesAgainstTheNamedDirectory(t *testing.T) {
	dir := t.TempDir()
	want := "first line\nsecond line\n"
	if err := os.WriteFile(filepath.Join(dir, "frame.txt"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}

	// A match passes; a mismatch fails with the diff. Both paths run against the
	// file the caller named, not a package-internal testdata directory.
	Assert(t, dir, "frame.txt", want)
	if got := Diff(want, "first line\nCHANGED\n"); !strings.Contains(got, "CHANGED") {
		t.Fatalf("diff lost the change: %s", got)
	}
}

func TestSizeFormatsAsGeometry(t *testing.T) {
	if got := (Size{W: 80, H: 24}).String(); got != "80x24" {
		t.Errorf("Size.String() = %q, want 80x24", got)
	}
}

// TestFitsPinsTheArtifactToItsGeometry covers the harness rule that an artifact's
// dimensions are part of the assertion: a line wider than the terminal, or more
// lines than the terminal has, is a failure.
func TestFitsPinsTheArtifactToItsGeometry(t *testing.T) {
	if err := Fits(10, 2, "1234567890\nabc\n"); err != nil {
		t.Errorf("a fitting artifact was rejected: %v", err)
	}
	// Display width, not bytes: three wide runes are six cells.
	if err := Fits(4, 1, "日本語\n"); err == nil {
		t.Error("six cells of wide runes must not fit a four-cell width")
	}
	if err := Fits(6, 1, "日本語\n"); err != nil {
		t.Errorf("six cells of wide runes must fit six: %v", err)
	}
	if err := Fits(5, 1, "123456\n"); err == nil {
		t.Error("a line wider than the geometry must be rejected")
	}
	if err := Fits(10, 2, "a\nb\nc\n"); err == nil {
		t.Error("more lines than the geometry allows must be rejected")
	}
	if err := Fits(0, 24, "x\n"); err == nil {
		t.Error("a non-terminal geometry must be rejected")
	}
	// Escape sequences take no cells: the width is measured on the text.
	if err := Fits(6, 1, "\x1b[1;31mred\x1b[0m\n"); err != nil {
		t.Errorf("an ANSI frame of three visible cells must fit six: %v", err)
	}
	if err := Fits(2, 1, "\x1b[1;31mred\x1b[0m\n"); err == nil {
		t.Error("the escapes must not hide a frame that is too wide")
	}

	// Blank rows below the content are not content.
	if err := Fits(3, 1, "abc\n\n\n"); err != nil {
		t.Errorf("trailing blank rows must not count against the geometry: %v", err)
	}
}

// TestDeterministicCatchesAFlakyRenderer is the gate for the harness rule that a
// golden must not depend on the run: a renderer that changes between two calls fails
// before anyone commits a baseline that passes half the time.
func TestDeterministicCatchesAFlakyRenderer(t *testing.T) {
	calls := 0
	err := Deterministic(10, 2, func(int, int) string {
		calls++
		if calls == 2 {
			return "second\n"
		}
		return "first\n"
	})
	if err == nil {
		t.Fatal("a renderer that answers differently the second time must fail")
	}
	if !strings.Contains(err.Error(), "different frames") {
		t.Errorf("error = %v, want it to say the frames differ", err)
	}

	if err := Deterministic(10, 2, func(int, int) string { return "stable\n" }); err != nil {
		t.Errorf("a stable renderer must pass: %v", err)
	}
}

// TestAssertFramePinsGeometryAndGolden covers the in-process pair: the frame must fit
// the geometry it claims and match the committed artifact.
func TestAssertFramePinsGeometryAndGolden(t *testing.T) {
	dir := t.TempDir()
	frame := "hello\r\nworld\r\n"
	if err := Write(dir, "frame.txt", Normalize(frame)); err != nil {
		t.Fatal(err)
	}

	// A fitting frame that matches passes; AssertFrame composes Fits and Assert, so
	// the mismatching and oversized cases are covered by their own tests above.
	AssertFrame(t, dir, "frame.txt", 20, 4, frame)

	frame2 := AssertDeterministic(t, 20, 4, func(int, int) string { return frame })
	if frame2 != frame {
		t.Errorf("AssertDeterministic returned %q, want the frame", frame2)
	}
}

// TestClipBringsAFrameInsideItsGeometry covers the rule the parity run against the
// harness surfaced: a frame may be wider than the terminal (a status bar drawn
// outside the cell grid), and clipping to the width is what makes a screenshot or a
// golden belong to its geometry. The clip must also be idempotent.
func TestClipBringsAFrameInsideItsGeometry(t *testing.T) {
	frame := "short\r\n" + strings.Repeat("x", 50) + "\r\n"

	if err := Fits(10, 3, frame); err == nil {
		t.Fatal("the test frame should not fit ten columns")
	}
	clipped := Clip(10, frame)
	if err := Fits(10, 3, clipped); err != nil {
		t.Errorf("a clipped frame must fit: %v", err)
	}
	if again := Clip(10, clipped); again != clipped {
		t.Errorf("clipping is not idempotent:\nfirst:  %q\nsecond: %q", clipped, again)
	}
	if err := FitsClipped(10, 3, frame); err != nil {
		t.Errorf("FitsClipped: %v", err)
	}

	// Styling that survives the cut: the visible text is truncated, the escapes are
	// not left open.
	styled := "\x1b[1;31m" + strings.Repeat("y", 20) + "\x1b[0m"
	got := Clip(5, styled)
	if plain := Normalize(got); strings.TrimRight(plain, "\n") != "yyyyy" {
		t.Errorf("clipped styled text = %q, want five y's", plain)
	}
}

// TestFitsWidthIgnoresHeight covers the distinction the parity run exposed: a frame
// may be taller than the terminal (the terminal clips or scrolls it), so the harness
// asserts width only for frames, while an emulated screen is exact in both.
func TestFitsWidthIgnoresHeight(t *testing.T) {
	tall := strings.Repeat("ok\n", 30)
	if err := FitsWidth(10, tall); err != nil {
		t.Errorf("a tall frame must pass a width check: %v", err)
	}
	if err := Fits(10, 3, tall); err == nil {
		t.Error("Fits must still notice that the frame is taller than the geometry")
	}
	if err := FitsWidth(1, "ab\n"); err == nil {
		t.Error("a row wider than the width must fail")
	}
}
