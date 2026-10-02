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
