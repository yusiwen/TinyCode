package tool

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzFuzzyFindInvariants pins the contract every edit strategy must honour:
// the search is deterministic, and a unique match reports a byte range that
// really holds the text it claims to have matched. The reported range is what
// the edit tool slices out of the file, so a wrong range silently corrupts the
// user's file.
func FuzzFuzzyFindInvariants(f *testing.F) {
	seeds := [][2]string{
		{"package main\n\nfunc main() {}\n", "func main() {}"},
		{"if x {\n    y()\n}\n", "if x {\n  y()\n}"},
		{"a\n\tb\n", "a\n  b"},
		{"x := \"\\n\"\n", "x := \"\\n\""},
		{"café naïve\n", "cafe naïve"},
		{"l1\nl2\nl3\n", "l1\nlX\nl3"},
		{"    indented\n", "indented"},
		{"aaaa", "aa"},
		{"same", "same"},
		{"", ""},
		{"", "x"},
		{"x", ""},
		{"\r\nwindows\r\n", "windows"},
		// Invalid UTF-8 in the file: the range must still sit on rune
		// boundaries instead of being reported as a broken match.
		{"if x {\n\xcby\n}", "if x {\n\n}"},
	}
	for _, s := range seeds {
		f.Add(s[0], s[1])
	}

	f.Fuzz(func(t *testing.T, content, search string) {
		got := fuzzyFind(content, search)
		if got == nil {
			t.Fatal("fuzzyFind returned nil; callers dereference the result")
		}
		if again := fuzzyFind(content, search); *again != *got {
			t.Fatalf("fuzzyFind is not deterministic: %+v vs %+v", *got, *again)
		}
		if got.count < 0 {
			t.Fatalf("negative match count %d (strategy %q)", got.count, got.strategy)
		}
		if got.count != 1 {
			// 0 means "not found" and >1 means "ambiguous": no range is promised.
			return
		}
		if got.start < 0 || got.end < got.start || got.end > len(content) {
			t.Fatalf("unique match range [%d,%d) is outside content of length %d (strategy %q)",
				got.start, got.end, len(content), got.strategy)
		}
		matched := content[got.start:got.end]
		if matched != got.matchText {
			t.Fatalf("range [%d,%d) holds %q but matchText is %q (strategy %q)",
				got.start, got.end, matched, got.matchText, got.strategy)
		}
		// For text that is valid UTF-8 — every real source file — the reported
		// range must lie on rune boundaries: offsets computed on normalized text
		// that cut a multi-byte sequence would hand the edit tool half a rune to
		// replace. An input that is already invalid has no rune boundaries to
		// honour, so only the range checks above apply to it.
		if !utf8.ValidString(content) {
			return
		}
		if got.start > 0 && !utf8.RuneStart(content[got.start]) {
			t.Fatalf("match starts inside a rune at offset %d (strategy %q)", got.start, got.strategy)
		}
		if got.end < len(content) && !utf8.RuneStart(content[got.end]) {
			t.Fatalf("match ends inside a rune at offset %d (strategy %q)", got.end, got.strategy)
		}
		if matched != "" && !utf8.ValidString(matched) {
			t.Fatalf("match split a UTF-8 character: %q (strategy %q)", matched, got.strategy)
		}
	})
}

// FuzzCorrectIndentation checks that re-indenting a replacement only rewrites
// leading whitespace: the words themselves must survive untouched.
func FuzzCorrectIndentation(f *testing.F) {
	f.Add("    foo()\n    bar()\n", "foo()\nbar()\n")
	f.Add("\tfoo", "foo")
	f.Add("  a", "b\n  c")
	f.Add("    x", "\n    y")
	f.Add("", "")
	f.Add("foo", "")
	f.Add("", "foo")

	f.Fuzz(func(t *testing.T, oldMatch, newString string) {
		got := correctIndentation(oldMatch, newString)

		if want, have := strings.Join(strings.Fields(newString), " "), strings.Join(strings.Fields(got), " "); want != have {
			t.Fatalf("content changed: %q -> %q (want words %q, got %q)", newString, got, want, have)
		}

		oldWS := leadingWhitespace(fuzzFirstLine(oldMatch))
		// The first line keeps its own indent when it already matches or when the
		// match is unindented; only a non-empty first line is re-indented.
		if oldWS != "" && fuzzFirstLine(newString) != "" && !strings.HasPrefix(got, oldWS) {
			t.Fatalf("first line %q lost the indentation %q of the matched line", got, oldWS)
		}
	})
}

// FuzzLevenshtein checks the similarity metric used by the block-anchor
// strategy: it must stay in [0,1], be symmetric and report identical strings as
// a perfect match.
func FuzzLevenshtein(f *testing.F) {
	f.Add("", "")
	f.Add("abc", "abc")
	f.Add("abc", "abd")
	f.Add("日本語", "日本")
	f.Add("a\nb\nc", "a\nx\nc")

	f.Fuzz(func(t *testing.T, a, b string) {
		if a == b && levenshteinSimilarity(a, b) != 1 {
			t.Fatalf("identical inputs scored %v", levenshteinSimilarity(a, b))
		}
		s := levenshteinSimilarity(a, b)
		if s < 0 || s > 1 {
			t.Fatalf("similarity %v out of [0,1] for %q,%q", s, a, b)
		}
		if d, r := levenshteinDistance(a, b), levenshteinDistance(b, a); d != r {
			t.Fatalf("distance is not symmetric: %d vs %d for %q,%q", d, r, a, b)
		}
	})
}

// FuzzRelBeneath checks the helper that decides whether a path is lexically
// beneath the sandbox root before the kernel is asked to confirm it. A wrong
// "true" would let the openat2 layer check the wrong path.
func FuzzRelBeneath(f *testing.F) {
	f.Add("/root/sub", "/root/sub/file.go")
	f.Add("/root/sub", "/root/sub/../other")
	f.Add("/root", "/root/../etc/passwd")
	f.Add("", "/etc/passwd")
	f.Add("/", "/anything")
	f.Add("root", "root")
	f.Add("a//b", "a/b/./c")
	f.Add("/root", "/rootx/file")

	f.Fuzz(func(t *testing.T, root, path string) {
		rel, ok := relBeneath(root, path)
		if !ok {
			return
		}
		rootParts := pathComponents(root)
		if len(pathComponents(path)) <= len(rootParts) {
			t.Fatalf("relBeneath(%q,%q) = %q: path has no component beyond the root", root, path, rel)
		}
		for i, part := range rootParts {
			if pathComponents(path)[i] != part {
				t.Fatalf("relBeneath(%q,%q) = %q: component %d differs", root, path, rel, i)
			}
		}
		// The remainder must describe exactly the components the caller saw.
		if got, want := pathComponents(root+"/"+rel), pathComponents(path); !reflect.DeepEqual(got, want) {
			t.Fatalf("relBeneath(%q,%q) = %q: rejoined components %v, want %v", root, path, rel, got, want)
		}
	})
}

// fuzzFirstLine returns the first line of s, including an empty string when s is
// empty. It exists so the fuzz targets do not depend on test helpers elsewhere.
func fuzzFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// TestFuzzyFindRejectsSplitRune pins the guard added after fuzzing found that a
// search holding a lone lead byte matched the first byte of a 3-byte character:
// replacing that range would have left the file with invalid UTF-8.
func TestFuzzyFindRejectsSplitRune(t *testing.T) {
	const content = "档" // e6 a3 a3
	r := fuzzyFind(content, "\xe6")
	if r.count != 0 {
		t.Fatalf("fuzzyFind reported %d match(es) for a split rune: %+v", r.count, *r)
	}
	// A well-formed search over the same file still matches the whole character.
	if r := fuzzyFind(content, content); r.count != 1 || r.start != 0 || r.end != len(content) {
		t.Fatalf("whole-character match = %+v", *r)
	}
}
