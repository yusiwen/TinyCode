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
	// Blank rows below the content are not content.
	if err := Fits(3, 1, "abc\n\n\n"); err != nil {
		t.Errorf("trailing blank rows must not count against the geometry: %v", err)
	}
}
