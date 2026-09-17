package tui

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzWordWrapPreservesWords checks the wrapping used for message text: it may
// move words to another chunk, but it must never invent, drop or reorder them,
// and no chunk may end in the blanks held between words.
func FuzzWordWrapPreservesWords(f *testing.F) {
	f.Add("hello world\n", 10)
	f.Add("a  b   c\n", 4)
	f.Add("    indented code line\n", 8)
	f.Add("oneverylongwordwithoutspaces\n", 5)
	f.Add("日本語 の テキスト\n", 6)
	f.Add("\n\n", 3)
	f.Add("", 1)
	f.Add("trailing   \n", 20)

	f.Fuzz(func(t *testing.T, text string, maxWidth int) {
		chunks := wordWrap(text, maxWidth, CellStyle{})

		parts := make([]string, 0, len(chunks))
		for i, c := range chunks {
			// A whitespace-only line keeps its indent, so the "never end a line
			// in blanks" rule only applies to chunks that carry words.
			if strings.TrimSpace(c.Text) != "" && strings.HasSuffix(c.Text, " ") {
				t.Fatalf("chunk %d ends in a space: %q", i, c.Text)
			}
			parts = append(parts, c.Text)
		}
		if got, want := strings.Join(strings.Fields(strings.Join(parts, "\n")), " "),
			strings.Join(strings.Fields(text), " "); got != want {
			t.Fatalf("wrapping changed the words:\n got %q\nwant %q", got, want)
		}
		// Every input line produces at least one chunk.
		if lines := strings.Count(text, "\n") + 1; len(chunks) < lines {
			t.Fatalf("wrapping produced %d chunks for %d lines", len(chunks), lines)
		}
	})
}

// FuzzParseMarkdown feeds arbitrary text — what a model can emit — through the
// markdown parser. It must not panic, and when the input is valid UTF-8 every
// string it produces must be too, because those strings are written straight
// into the cell grid.
func FuzzParseMarkdown(f *testing.F) {
	f.Add("# Heading\n\nText with `code` and **bold**.\n")
	f.Add("- a\n- b\n  - nested\n\n1. one\n2. two\n")
	f.Add("| a | b |\n|---|---|\n| 1 | 2 |\n")
	f.Add("> quote\n> more\n\n```go\nfunc main() {}\n```\n")
	f.Add("[link](https://example.com) ![img](x.png)\n")
	f.Add("---\n***\n___\n")
	f.Add("中文 **粗体** 与 `コード`\n")
	f.Add("\x00\x01\x02")

	f.Fuzz(func(t *testing.T, md string) {
		blocks := parseMarkdown(md)
		if !utf8.ValidString(md) {
			return // invalid input may be reproduced verbatim in Code fields
		}
		var check func(bs []ContentBlock)
		check = func(bs []ContentBlock) {
			for _, b := range bs {
				if !utf8.ValidString(b.Code) {
					t.Fatalf("block %q produced invalid UTF-8 code %q", b.Type, b.Code)
				}
				for _, c := range b.Chunks {
					if !utf8.ValidString(c.Text) {
						t.Fatalf("block %q produced invalid UTF-8 text %q", b.Type, c.Text)
					}
				}
				for _, row := range b.Rows {
					for _, cell := range row {
						for _, c := range cell {
							if !utf8.ValidString(c.Text) {
								t.Fatalf("table cell produced invalid UTF-8 text %q", c.Text)
							}
						}
					}
				}
				check(b.Items)
			}
		}
		check(blocks)
	})
}
