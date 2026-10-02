package font

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"

	"github.com/yusiwen/TinyCode/tuiprobe/screen"
)

// feed replays a stream into a buffer of the given size.
func feed(t *testing.T, cols, rows int, stream string) *screen.Buffer {
	t.Helper()
	b := screen.New(cols, rows)
	if _, err := b.Write([]byte(stream)); err != nil {
		t.Fatalf("feed: %v", err)
	}
	return b
}

func TestImageSizeIsTheGeometryTimesTheCell(t *testing.T) {
	r, err := New(Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cellW, cellH := r.CellSize()
	if cellW <= 0 || cellH <= 0 {
		t.Fatalf("cell size = %dx%d, want positive", cellW, cellH)
	}

	b := feed(t, 6, 3, "hello\r\nworld\r\n")
	img := r.Image(b)
	if got, want := img.Bounds().Dx(), 6*cellW; got != want {
		t.Errorf("width = %d, want %d (6 columns × %d)", got, want, cellW)
	}
	if got, want := img.Bounds().Dy(), 3*cellH; got != want {
		t.Errorf("height = %d, want %d (3 rows × %d)", got, want, cellH)
	}
}

// TestScaleEnlargesGlyphsAndCells: the scale multiplies the face, so the cells grow
// with the text instead of leaving tiny glyphs in large boxes.
func TestScaleEnlargesGlyphsAndCells(t *testing.T) {
	one, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	two, err := New(Options{Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	w1, h1 := one.CellSize()
	w2, h2 := two.CellSize()
	if w2 <= w1 || h2 <= h1 {
		t.Fatalf("scale 2 cell = %dx%d, scale 1 cell = %dx%d; the second must be larger", w2, h2, w1, h1)
	}

	b := feed(t, 2, 1, "ab")
	if got, want := two.Image(b).Bounds().Dx(), 2*w2; got != want {
		t.Errorf("scaled width = %d, want %d", got, want)
	}
}

// TestPixelsCarryTheStyle is the assertion that makes the image evidence: the
// foreground colour must actually land in the cell, the background behind it, and
// the underline at the bottom of the cell's box.
func TestPixelsCarryTheStyle(t *testing.T) {
	r, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	cellW, cellH := r.CellSize()
	b := feed(t, 3, 1, "\x1b[38;2;255;0;0;48;2;0;0;255;4mX\x1b[0m")

	img := r.Image(b)
	if got := img.RGBAAt(0, 0); got.B != 0xff || got.R != 0 {
		t.Errorf("cell background = %v, want blue", got)
	}

	// Somewhere in the cell's box there must be a red pixel: that is the glyph.
	red := color.RGBA{R: 0xff, A: 0xff}
	found := false
	for y := 0; y < cellH && !found; y++ {
		for x := 0; x < cellW; x++ {
			if img.RGBAAt(x, y) == red {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("no foreground pixel was drawn for the styled glyph")
	}

	// The underline occupies the bottom row of the cell.
	if got := img.RGBAAt(cellW/2, cellH-1); got != red {
		t.Errorf("underline pixel = %v, want the foreground colour", got)
	}

	// A cell that was never drawn keeps the terminal background.
	blank := image.Rect(cellW, 0, cellW*3, cellH)
	outside := img.RGBAAt(blank.Min.X+1, 1)
	bg := color.RGBAModel.Convert(r.bg).(color.RGBA)
	if outside != bg {
		t.Errorf("untouched cell = %v, want the terminal background %v", outside, bg)
	}
}

// TestMissingGlyphsAreVisible: a face without a rune must not silently drop it.
func TestMissingGlyphsAreVisible(t *testing.T) {
	r, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	cellW, cellH := r.CellSize()
	b := feed(t, 1, 1, "\U0001F600") // an emoji Go Mono does not carry

	img := r.Image(b)
	fg := color.RGBAModel.Convert(r.fg).(color.RGBA)
	found := false
	for y := 0; y < cellH && !found; y++ {
		for x := 0; x < cellW; x++ {
			if img.RGBAAt(x, y) == fg {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("a missing glyph left the cell empty; a placeholder must show")
	}
}

func TestPNGRoundTripsThroughTheDecoder(t *testing.T) {
	r, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	cellW, cellH := r.CellSize()
	data, width, height, err := r.PNG(feed(t, 4, 2, "ok\r\ngo"))
	if err != nil {
		t.Fatalf("PNG: %v", err)
	}
	if width != 4*cellW || height != 2*cellH {
		t.Errorf("reported size = %dx%d, want %dx%d", width, height, 4*cellW, 2*cellH)
	}
	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if format != "png" {
		t.Errorf("format = %q, want png", format)
	}
	if decoded.Bounds().Dx() != width || decoded.Bounds().Dy() != height {
		t.Errorf("decoded size = %v, want %dx%d", decoded.Bounds().Size(), width, height)
	}

	// And it is a real PNG: the magic number, not an empty buffer.
	if !bytes.HasPrefix(data, []byte{0x89, 'P', 'N', 'G'}) {
		t.Error("the output does not start with the PNG signature")
	}
	_ = png.BestCompression
}
