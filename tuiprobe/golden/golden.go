// Package golden compares a rendered artifact — a terminal frame, an emulated
// screen dump, an ANSI snapshot — against a file committed next to the test, and
// rewrites that file when the run asks for it (`go test -update`).
//
// A missing file is a failure, never an implicit write: a test that silently
// creates its own expectation passes forever without asserting anything.
package golden

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Update reports whether the test binary was run with -update.
//
// The flag is looked up rather than registered here so importing this package
// does not add a flag to the host program: a `go test` user registers `-update`
// once and every call site sees it.
func Update() bool {
	if f := flag.Lookup("update"); f != nil {
		return f.Value.String() == "true"
	}
	return false
}

// RegisterFlag declares `-update` for programs that do not already have it, so a
// caller can offer the conventional flag without knowing this package exists. It
// returns nil when the flag already exists.
func RegisterFlag(usage string) *bool {
	if flag.Lookup("update") != nil {
		return nil
	}
	return flag.Bool("update", false, usage)
}

var (
	// csiSequence matches a CSI escape (ESC [ ... final byte).
	csiSequence = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]")
	// oscSequence matches an OSC escape (ESC ] ... BEL, or ESC ] ... ESC \),
	// which is how a terminal carries a hyperlink target or a title.
	oscSequence = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")
)

// Normalize reduces an artifact to its reviewable text form: escape sequences
// stripped, carriage returns removed, trailing blanks trimmed per line and
// trailing blank lines dropped. Two artifacts that differ only in fixed-width
// padding therefore compare equal.
func Normalize(artifact string) string {
	plain := oscSequence.ReplaceAllString(artifact, "")
	plain = csiSequence.ReplaceAllString(plain, "")
	plain = strings.ReplaceAll(plain, "\r\n", "\n")
	lines := strings.Split(plain, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// Diff reports the first line that differs, with one line of context and the
// first differing column, so a failure reads like a review comment instead of a
// wall of text.
func Diff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	limit := len(wantLines)
	if len(gotLines) > limit {
		limit = len(gotLines)
	}
	for i := 0; i < limit; i++ {
		var w, g string
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w == g {
			continue
		}
		col := 0
		for col < len(w) && col < len(g) && w[col] == g[col] {
			col++
		}
		var b strings.Builder
		fmt.Fprintf(&b, "first differing line %d (column %d):\n", i+1, col+1)
		if i > 0 {
			fmt.Fprintf(&b, "  context: %q\n", wantLines[i-1])
		}
		fmt.Fprintf(&b, "  want: %q\n", w)
		fmt.Fprintf(&b, "  got:  %q\n", g)
		return b.String()
	}
	return "artifacts differ only in trailing blank lines\n"
}

// Assert compares got against dir/name, writing the file when Update() is set.
//
// The caller names the directory explicitly: a golden belongs to the project
// that committed it, not to this package.
func Assert(t testing.TB, dir, name, got string) {
	t.Helper()
	path := filepath.Join(dir, name)

	if Update() {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\nregenerate it with: go test ./... -update", path, err)
	}
	if string(want) == got {
		return
	}
	t.Errorf("artifact does not match %s\n%s", path, Diff(string(want), got))
}

// Size is one terminal geometry an artifact is captured at.
type Size struct {
	W, H int
}

func (s Size) String() string { return fmt.Sprintf("%dx%d", s.W, s.H) }
