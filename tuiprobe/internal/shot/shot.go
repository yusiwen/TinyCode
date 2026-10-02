// Package shot turns what is on a session's screen into an artifact: a PNG, or the
// HTML a browser can render.
//
// It is the bridge between the emulator (which knows the cell grid and its styles)
// and the two front ends (the CLI's `shot` command and the scenario's
// `screenshot` step), so the geometry assertion is written once.
package shot

import (
	"bytes"
	"fmt"
	"image"
	_ "image/png" // register the decoder for DecodeConfig
	"os"

	"github.com/yusiwen/TinyCode/tuiprobe/render/font"
	"github.com/yusiwen/TinyCode/tuiprobe/screen"
)

// Formats a screenshot can take.
const (
	FormatPNG  = "png"
	FormatHTML = "html"
)

// Options configure one capture.
type Options struct {
	// Format is FormatPNG (the default) or FormatHTML.
	Format string
	// Scale enlarges the font renderer's face.
	Scale int
	// FontPath is a TTF/OTF to render with instead of the embedded face.
	FontPath string
}

// Artifact is a rendered screen.
type Artifact struct {
	Data          []byte
	Format        string
	Width, Height int // pixels for PNG; cells for HTML
}

// Render replays a screen's ANSI form and renders it.
//
// The ANSI round trip is deliberate: the daemon already sends a screen as ANSI, and
// replaying it through the emulator rebuilds the exact cell grid and styles — the
// same path `screen.Render` is round-trip tested against — so the protocol stays
// small and the renderer stays a pure function of a screen.
func Render(ansi string, cols, rows int, opts Options) (Artifact, error) {
	format := opts.Format
	if format == "" {
		format = FormatPNG
	}

	terminal := screen.New(cols, rows)
	if _, err := terminal.Write([]byte(ansi)); err != nil {
		return Artifact{}, fmt.Errorf("shot: replay the screen: %w", err)
	}

	switch format {
	case FormatHTML:
		return Artifact{
			Data:   []byte(terminal.HTML()),
			Format: FormatHTML,
			Width:  cols,
			Height: rows,
		}, nil
	case FormatPNG:
		var fontData []byte
		if opts.FontPath != "" {
			data, err := os.ReadFile(opts.FontPath)
			if err != nil {
				return Artifact{}, fmt.Errorf("shot: read font: %w", err)
			}
			fontData = data
		}
		renderer, err := font.New(font.Options{FontData: fontData, Scale: opts.Scale})
		if err != nil {
			return Artifact{}, err
		}
		data, width, height, err := renderer.PNG(terminal)
		if err != nil {
			return Artifact{}, fmt.Errorf("shot: encode png: %w", err)
		}

		// The geometry assertion: the image must be exactly columns × cell width by
		// rows × cell height. An image whose size is not a function of the geometry
		// is not evidence of that geometry.
		cellW, cellH := renderer.CellSize()
		if want := cols * cellW; width != want {
			return Artifact{}, fmt.Errorf("shot: image is %d pixels wide, want %d (%d columns × %d)", width, want, cols, cellW)
		}
		if want := rows * cellH; height != want {
			return Artifact{}, fmt.Errorf("shot: image is %d pixels tall, want %d (%d rows × %d)", height, want, rows, cellH)
		}
		return Artifact{Data: data, Format: FormatPNG, Width: width, Height: height}, nil
	default:
		return Artifact{}, fmt.Errorf("shot: unknown format %q (want png or html)", format)
	}
}

// Write stores an artifact.
func Write(path string, a Artifact) error {
	if err := os.WriteFile(path, a.Data, 0o644); err != nil {
		return fmt.Errorf("shot: write %s: %w", path, err)
	}
	return nil
}

// decodePNGSize reads the real size of PNG bytes, without trusting what the
// renderer reported: the assertion has to be about the artifact on disk.
func decodePNGSize(data []byte) (image.Point, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Point{}, err
	}
	return image.Pt(cfg.Width, cfg.Height), nil
}

// VerifyPNG checks that a PNG file on disk has the size the geometry implies.
func VerifyPNG(path string, cols, rows int, opts Options) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	size, err := decodePNGSize(data)
	if err != nil {
		return fmt.Errorf("shot: %s is not a decodable PNG: %w", path, err)
	}
	cellW, cellH := 0, 0
	{
		renderer, err := font.New(font.Options{Scale: opts.Scale})
		if err != nil {
			return err
		}
		cellW, cellH = renderer.CellSize()
	}
	if size.X != cols*cellW || size.Y != rows*cellH {
		return fmt.Errorf("shot: %s is %dx%d, want %dx%d for a %dx%d screen", path, size.X, size.Y, cols*cellW, rows*cellH, cols, rows)
	}
	return nil
}
