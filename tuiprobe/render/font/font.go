// Package font renders an emulated screen to a PNG without a browser.
//
// The alternative — building HTML and screenshotting it with Chromium — gives
// pixel-exact CSS and system fonts, but it costs a browser download, a launch per
// image and a pile of sandbox flags. This renderer draws the cell grid directly:
// every cell is a rectangle, every rune is drawn at its cell's baseline, and the
// arithmetic is exact, so the image's size is a property of the geometry rather
// than of a font stack.
//
// The default face is Go Mono (embedded in golang.org/x/image), so the package
// needs no font file. A caller who wants box-drawing or icon glyphs points it at a
// TTF of its own.
package font

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"strconv"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/yusiwen/TinyCode/tuiprobe/screen"
)

// DefaultFontSize is the face size in points when the caller does not choose one.
const DefaultFontSize = 16

// DefaultBackground is used for cells with no background colour of their own.
var DefaultBackground = color.RGBA{R: 0x1c, G: 0x1c, B: 0x1c, A: 0xff}

// DefaultForeground is used for cells with no foreground colour of their own.
var DefaultForeground = color.RGBA{R: 0xd0, G: 0xd0, B: 0xd0, A: 0xff}

// Options configure a render.
type Options struct {
	// FontData is a TTF/OTF to render with; nil uses the embedded Go Mono.
	FontData []byte
	// FontSize is the point size; 0 means DefaultFontSize.
	FontSize float64
	// Scale multiplies the pixels (2 gives a retina-sized image); 0 means 1.
	Scale int
	// Background/Foreground override the terminal defaults.
	Background, Foreground color.Color
	// CellWidth/CellHeight override the measured cell size, for a caller that
	// wants its images to line up with another renderer's.
	CellWidth, CellHeight int
}

// Renderer holds a parsed face and the cell metrics measured from it.
type Renderer struct {
	face   font.Face
	cellW  int
	cellH  int
	ascent int
	scale  int
	bg     color.Color
	fg     color.Color
}

// New prepares a renderer.
func New(opts Options) (*Renderer, error) {
	data := opts.FontData
	if len(data) == 0 {
		data = gomono.TTF
	}
	parsed, err := opentype.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("font: parse: %w", err)
	}
	size := opts.FontSize
	if size <= 0 {
		size = DefaultFontSize
	}
	scale := opts.Scale
	if scale <= 0 {
		scale = 1
	}
	// Scale is applied to the face, not to the pixels: a bigger face gives bigger
	// glyphs *and* bigger cells, so the image stays a faithful enlargement instead
	// of tiny text in large boxes.
	size *= float64(scale)
	face, err := opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, fmt.Errorf("font: face: %w", err)
	}

	metrics := face.Metrics()
	advance, _ := face.GlyphAdvance('M')
	cellW := opts.CellWidth
	if cellW <= 0 {
		cellW = advance.Ceil()
	}
	cellH := opts.CellHeight
	if cellH <= 0 {
		cellH = metrics.Height.Ceil()
	}
	if cellW <= 0 || cellH <= 0 {
		return nil, fmt.Errorf("font: the face reports cell size %dx%d", cellW, cellH)
	}

	bg, fg := opts.Background, opts.Foreground
	if bg == nil {
		bg = DefaultBackground
	}
	if fg == nil {
		fg = DefaultForeground
	}
	return &Renderer{
		face: face, cellW: cellW, cellH: cellH, ascent: metrics.Ascent.Ceil(),
		scale: scale, bg: bg, fg: fg,
	}, nil
}

// CellSize is the pixel size of one cell: the number a caller asserts an image
// against, since the image's width must be columns × this.
func (r *Renderer) CellSize() (w, h int) { return r.cellW, r.cellH }

// Image renders a screen.
func (r *Renderer) Image(b *screen.Buffer) *image.RGBA {
	cols, rows := b.Bounds()
	w, h := r.CellSize()
	img := image.NewRGBA(image.Rect(0, 0, cols*w, rows*h))
	draw.Draw(img, img.Bounds(), image.NewUniform(r.bg), image.Point{}, draw.Src)

	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col++ {
			r.drawCell(img, b, col, row)
		}
	}
	return img
}

// PNG renders a screen and returns the encoded bytes with their size.
func (r *Renderer) PNG(b *screen.Buffer) ([]byte, int, int, error) {
	img := r.Image(b)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, 0, 0, err
	}
	return buf.Bytes(), img.Bounds().Dx(), img.Bounds().Dy(), nil
}

// EncodePNG writes a screen as PNG to w.
func (r *Renderer) EncodePNG(w io.Writer, b *screen.Buffer) (int, int, error) {
	img := r.Image(b)
	if err := png.Encode(w, img); err != nil {
		return 0, 0, err
	}
	return img.Bounds().Dx(), img.Bounds().Dy(), nil
}

func (r *Renderer) drawCell(img *image.RGBA, b *screen.Buffer, col, row int) {
	glyph, style, ok := b.CellAt(col, row)
	w, h := r.CellSize()
	x, y := col*w, row*h
	cell := image.Rect(x, y, x+w, y+h)

	if bg := colorOf(style.BG, r.bg); bg != nil {
		draw.Draw(img, cell, image.NewUniform(bg), image.Point{}, draw.Src)
	}
	if !ok {
		return
	}
	fg := colorOf(style.FG, r.fg)
	if style.Dim {
		fg = blend(fg, r.bg, 0.5)
	}

	dot := fixed.Point26_6{
		X: fixed.I(x),
		Y: fixed.I(y + r.ascent),
	}
	drawer := &font.Drawer{Dst: img, Src: image.NewUniform(fg), Face: r.face, Dot: dot}
	if _, _, hasGlyph := r.face.GlyphBounds(glyph); hasGlyph {
		drawer.DrawString(string(glyph))
		if style.Bold {
			// Go Mono has no bold face; drawing the glyph twice, one pixel apart,
			// is the same trick terminals use to fake it.
			drawer.Dot.X += fixed.I(1)
			drawer.DrawString(string(glyph))
		}
	} else {
		// Say "missing glyph" instead of drawing nothing: an image that silently
		// drops characters is worse than one that shows a placeholder.
		box := image.Rect(x+w/4, y+h/4, x+w-w/4, y+h-h/4)
		draw.Draw(img, box, image.NewUniform(fg), image.Point{}, draw.Src)
	}
	if style.Underline {
		thickness := h / 16
		if thickness < 1 {
			thickness = 1
		}
		line := image.Rect(x, y+h-thickness, x+w, y+h)
		draw.Draw(img, line, image.NewUniform(fg), image.Point{}, draw.Src)
	}
}

// colorOf parses a #rrggbb string, falling back when it is empty or unreadable.
func colorOf(css string, fallback color.Color) color.Color {
	if css == "" {
		return fallback
	}
	hex := strings.TrimPrefix(css, "#")
	if len(hex) != 6 {
		return fallback
	}
	value, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return fallback
	}
	return color.RGBA{
		R: uint8(value >> 16),
		G: uint8(value >> 8),
		B: uint8(value),
		A: 0xff,
	}
}

// blend mixes two colours, used for the dim attribute.
func blend(from, to color.Color, amount float64) color.Color {
	fr, fg, fb, _ := from.RGBA()
	tr, tg, tb, _ := to.RGBA()
	mix := func(a, b uint32) uint8 {
		return uint8(float64(a)*(1-amount) + float64(b)*amount)
	}
	return color.RGBA{R: mix(fr>>8, tr>>8), G: mix(fg>>8, tg>>8), B: mix(fb>>8, tb>>8), A: 0xff}
}
