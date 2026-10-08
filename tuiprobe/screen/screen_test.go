package screen

import (
	"strings"
	"testing"
)

// replayScreen feeds a captured terminal stream through a fresh buffer.
func replayScreen(stream string, width, height int) *Buffer {
	s := New(width, height)
	_, _ = s.Write([]byte(stream))
	return s
}

// TestBufferReplaysCRLFFrames is the fidelity check that needs no PTY: a frame
// whose rows are separated by CRLF (what a renderer writes, measured from a
// captured stream) must survive the round trip through the emulator. A bare LF
// would leave the cursor in the last column, which is why callers normalize.
func TestBufferReplaysCRLFFrames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rows  int
		frame string
		want  string
	}{
		{"plain", 2, "hello\r\nworld", "hello\nworld\n"},
		{"styled", 1, "\x1b[1;31mred\x1b[0m plain", "red plain\n"},
		{"erased", 2, "aaa\r\nbbb\x1b[1A\rXXX", "XXX\nbbb\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			screen := replayScreen(tc.frame, 20, tc.rows)
			if got := screen.String(); got != tc.want {
				t.Errorf("replayed screen = %q, want %q", got, tc.want)
			}
		})
	}
}

// Styling must survive the replay: a screenshot taken from the stream is only
// worth as much as the styles it kept.
func TestBufferKeepsStylingForHTML(t *testing.T) {
	screen := replayScreen("\x1b[1;31mred\x1b[0m", 10, 1)
	if !strings.Contains(screen.HTML(), "<span style=") {
		t.Errorf("replayed screen lost its styling")
	}
	if !strings.Contains(screen.HTML(), "#d75f5f") {
		t.Errorf("the SGR colour did not reach the HTML: %s", screen.HTML())
	}
}

func TestBufferCursorUpRepaints(t *testing.T) {
	s := New(5, 3)
	_, _ = s.Write([]byte("aaa\r\nbbb\r\nccc"))
	if got := firstRow(s); got != "aaa" {
		t.Fatalf("row 0 = %q, want aaa", got)
	}
	_, _ = s.Write([]byte("\x1b[2A\rXXX"))
	if got := firstRow(s); got != "XXX" {
		t.Errorf("row 0 after repaint = %q, want XXX", got)
	}
	if got := strings.Split(s.String(), "\n")[2]; got != "ccc" {
		t.Errorf("row 2 = %q, want ccc (the repaint touched the wrong row)", got)
	}
}

func TestBufferErasesStaleText(t *testing.T) {
	s := New(10, 2)
	_, _ = s.Write([]byte("abcdefghij\r\nkl"))
	_, _ = s.Write([]byte("\x1b[1A\r\x1b[K"))
	if got := firstRow(s); got != "" {
		t.Errorf("row 0 = %q, want it erased end-of-line", got)
	}
	if got := strings.Split(s.String(), "\n")[1]; got != "kl" {
		t.Errorf("row 1 = %q, want kl", got)
	}
}

func TestBufferScrolls(t *testing.T) {
	s := New(4, 2)
	_, _ = s.Write([]byte("aaa\r\nbbb\r\nccc"))
	rows := strings.Split(strings.TrimRight(s.String(), "\n"), "\n")
	if len(rows) != 2 || rows[0] != "bbb" || rows[1] != "ccc" {
		t.Errorf("rows after scrolling = %q, want [bbb ccc]", rows)
	}
}

func TestBufferWrapsAtTheLastColumn(t *testing.T) {
	// A full row plus one more character scrolls a two-row screen: the terminal
	// wraps on the next printable rune, exactly like the renderer relies on.
	s := New(4, 2)
	_, _ = s.Write([]byte("one\r\ntwo\r\nthree"))
	rows := strings.Split(strings.TrimRight(s.String(), "\n"), "\n")
	if len(rows) != 2 || rows[0] != "thre" || rows[1] != "e" {
		t.Errorf("rows = %q, want [thre e]", rows)
	}
}

func TestBufferWideRunes(t *testing.T) {
	s := New(6, 1)
	_, _ = s.Write([]byte("中文x"))
	if got := firstRow(s); got != "中文x" {
		t.Errorf("row = %q, want 中文x", got)
	}
	// A double-width rune must not leave a stray continuation cell in the
	// HTML: the body is exactly the three runes.
	if body := s.HTML(); strings.Count(body, "中") != 1 || strings.Count(body, "文") != 1 {
		t.Errorf("wide runes were duplicated in the HTML: %q", body)
	}
}

func TestBufferIgnoresQueriesAndModes(t *testing.T) {
	s := New(10, 1)
	_, _ = s.Write([]byte("\x1b]11;?\x1b\\\x1b[6n\x1b[?25l\x1b[?2004hAB"))
	if got := firstRow(s); got != "AB" {
		t.Errorf("row = %q, want AB (queries and mode changes carry no text)", got)
	}
}

func TestBufferEraseDisplay(t *testing.T) {
	s := New(5, 2)
	_, _ = s.Write([]byte("abc\r\ndef\x1b[2J"))
	if got := strings.TrimSpace(s.String()); got != "" {
		t.Errorf("screen = %q, want it cleared", got)
	}
}

// firstRow returns row 0 with trailing blanks removed.
func firstRow(s *Buffer) string {
	return strings.Split(s.String(), "\n")[0]
}

// --- Live-stream screenshot (gated) ---------------------------------------

// stableBannerRows picks the welcome-frame rows whose text cannot depend on the
// build (tool/skill/agent counts) or on the wall clock (session duration): the
// wordmark, the shortcut block, the footer and anything else without a digit.
// Those rows must survive the round trip through a real terminal byte for byte;
// the counts row and the status bar legitimately differ in a live run.

// TestBufferRenderRoundTripsThroughTheEmulator is the property that makes ANSI
// output trustworthy: rendering a screen and feeding it back must reproduce the
// same text and the same styles.
func TestBufferRenderRoundTripsThroughTheEmulator(t *testing.T) {
	stream := "\x1b[1;31mred bold\x1b[0m plain\r\n" +
		"\x1b[4;38;5;208munderline 256\x1b[0m\r\n" +
		"\x1b[38;2;10;200;30mtruecolor\x1b[0m"
	first := replayScreen(stream, 30, 3)

	rendered := first.Render()
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("Render produced no escapes: %q", rendered)
	}

	second := replayScreen(rendered, 30, 3)
	if got, want := second.String(), first.String(); got != want {
		t.Errorf("round trip changed the text\n got: %q\nwant: %q", got, want)
	}
	if got, want := second.HTML(), first.HTML(); got != want {
		t.Errorf("round trip changed the styling\n got: %s\nwant: %s", got, want)
	}
}

// TestStyleSGRKeepsTheBasePalette checks the encoding the round trip depends on:
// the 16 base colours must come back as their own codes, not as truecolor.
func TestStyleSGRKeepsTheBasePalette(t *testing.T) {
	plain := Style{}
	if got, want := sgrFor(plain), "\x1b[0m"; got != want {
		t.Errorf("plain style = %q, want %q", got, want)
	}
	bright := Style{FG: ansiPalette[9]}
	if got, want := sgrFor(bright), "\x1b[0;91m"; got != want {
		t.Errorf("bright red = %q, want %q", got, want)
	}
	cube := Style{FG: xterm256ToCSS(208)}
	if got, want := sgrFor(cube), "\x1b[0;38;5;208m"; got != want {
		t.Errorf("256-colour = %q, want %q", got, want)
	}
}

// TestEscapeSplitAcrossWritesSurvives is issue #75. Terminal output arrives in
// arbitrary chunks, and a chunk boundary can fall inside an escape sequence: the
// leading half used to be dropped and the trailing half drawn as text, so a live
// capture read "github.com/yusiw5;255;4men/Tin" while a single-write replay of the
// same bytes was perfect.
func TestEscapeSplitAcrossWritesSurvives(t *testing.T) {
	// The shape TinyCode emits for one hyperlinked character: an OSC 8 opener, then
	// per-character SGR.
	stream := "\x1b]8;;https://github.com/yusiwen/TinyCode\x1b\\" +
		"\x1b[4;38;2;0;255;255;4m" + "TinyCode" + "\x1b[0m"

	reference := New(80, 3)
	if _, err := reference.Write([]byte(stream)); err != nil {
		t.Fatal(err)
	}
	want := reference.String()

	for split := 0; split <= len(stream); split++ {
		b := New(80, 3)
		if _, err := b.Write([]byte(stream[:split])); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Write([]byte(stream[split:])); err != nil {
			t.Fatal(err)
		}
		if got := b.String(); got != want {
			t.Fatalf("split at byte %d: got %q, want %q", split, got, want)
		}
	}
}

// TestLoneEscapeIsNotText: an ESC with nothing after it must not appear as a glyph,
// and the next write must complete it rather than start over.
func TestLoneEscapeIsNotText(t *testing.T) {
	b := New(20, 2)
	if _, err := b.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write([]byte("[31mred")); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); !strings.Contains(got, "red") || strings.Contains(got, "[31m") {
		t.Errorf("screen = %q, want the styled text without the escape", got)
	}
}

// TestStringSinceTellsNewTextFromTheFramesBeforeIt is the screen-level half of
// issue #126: a second process can draw exactly the same frame as the first, so the
// text on screen cannot say whether it is new. The generation can.
func TestStringSinceTellsNewTextFromTheFramesBeforeIt(t *testing.T) {
	b := New(20, 3)
	_, _ = b.Write([]byte("stale\r\n"))
	mark := b.Generation()

	if got := strings.TrimSpace(b.StringSince(mark)); got != "" {
		t.Errorf("StringSince(mark) = %q, want nothing: no write happened after the mark", got)
	}
	_, _ = b.Write([]byte("fresh\r\n"))
	if got := b.StringSince(mark); !strings.Contains(got, "fresh") || strings.Contains(got, "stale") {
		t.Errorf("StringSince(mark) = %q, want only the text written after it", got)
	}
	if got := b.String(); !strings.Contains(got, "stale") || !strings.Contains(got, "fresh") {
		t.Errorf("String() = %q, want the whole screen: the filter must not change it", got)
	}
}

// TestStringSinceIsPerCell: the filter is the age of each cell, not of the screen, so
// text rewritten in place counts while the cells around it stay left over.
func TestStringSinceIsPerCell(t *testing.T) {
	b := New(10, 1)
	_, _ = b.Write([]byte("aaaaaaaaaa"))
	mark := b.Generation()
	_, _ = b.Write([]byte("\x1b[9Gbb")) // column 9, then two cells

	got := strings.TrimRight(b.StringSince(mark), "\n")
	if want := "        bb"; got != want {
		t.Errorf("StringSince = %q, want %q: only the two cells written after the mark count", got, want)
	}
}

// TestStringSinceSurvivesScrolling: a row that scrolls up carries the age of its
// cells with it, so text written before the mark stays old wherever it lands.
func TestStringSinceSurvivesScrolling(t *testing.T) {
	b := New(10, 2)
	_, _ = b.Write([]byte("keep\r\nx"))
	mark := b.Generation()
	_, _ = b.Write([]byte("\r\nnew")) // scrolls "x" up to row 0, draws "new" on row 1

	if got, want := b.String(), "x\nnew\n"; got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
	if got, want := b.StringSince(mark), "\nnew\n"; got != want {
		t.Errorf("StringSince = %q, want %q: the scrolled cell is still old", got, want)
	}
}

// TestResetKeepsTheGeneration: a resize re-lays-out the buffer, and a mark taken
// before it must stay meaningful — a fresh counter behind the mark would match
// nothing for ever, or the old screen again.
func TestResetKeepsTheGeneration(t *testing.T) {
	b := New(10, 2)
	_, _ = b.Write([]byte("before"))
	mark := b.Generation()

	b.Reset(20, 3)
	if b.Generation() != mark {
		t.Errorf("generation after Reset = %d, want %d", b.Generation(), mark)
	}
	if cols, rows := b.Bounds(); cols != 20 || rows != 3 {
		t.Errorf("bounds after Reset = %dx%d, want 20x3", cols, rows)
	}
	if got := strings.TrimSpace(b.StringSince(mark)); got != "" {
		t.Errorf("StringSince(mark) after Reset = %q, want nothing", got)
	}
	_, _ = b.Write([]byte("after"))
	if got := b.StringSince(mark); !strings.Contains(got, "after") {
		t.Errorf("StringSince(mark) after Reset and a write = %q, want the new text", got)
	}
}
