package chromium

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"image"
	_ "image/png" // register the PNG decoder this package reads screenshots with
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/yusiwen/TinyCode/tuiprobe/screen"
)

// CellWidth and CellHeight are the CSS pixel box every terminal cell gets.
//
// They are fixed numbers rather than font metrics on purpose: the pixel size then
// follows from the geometry alone, which is what makes an image's dimensions an
// assertion rather than a property of whatever font the machine has.
const (
	CellWidth  = 9
	CellHeight = 18
)

// DefaultTimeout bounds one browser run. A page that never paints must fail rather
// than hold a tool call open.
var DefaultTimeout = 30 * time.Second

// Options configure a capture.
type Options struct {
	// Browser is an explicit browser path; empty means discover one.
	Browser string
	// Scale multiplies the CSS pixel box (device scale factor).
	Scale int
	// Timeout bounds the browser run; 0 means DefaultTimeout.
	Timeout time.Duration
	// ExtraArgs are appended to the browser command line.
	ExtraArgs []string
}

// Available reports whether a browser can be used, and discovers it.
func Available() bool { return Default.Find() != "" }

// Bounds is the pixel range an image of this geometry may have.
//
// Unlike the pure-Go renderer, a browser's screenshot is not exactly predictable:
// scrollbars, the window chrome and the device scale factor all move it by a few
// pixels. The harness learned to assert a range for exactly this reason, and the
// range is narrow enough to catch a page that widened to its longest line.
func Bounds(cols, rows, scale int) (minW, maxW, minH, maxH int) {
	if scale <= 0 {
		scale = 1
	}
	width := cols * CellWidth * scale
	height := rows * CellHeight * scale
	// The +24 the harness used: enough for a scrollbar and rounding, far too little
	// for a page that ignored the geometry.
	return width, width + 24, height, height + 24
}

// Renderer turns screens into images through a browser.
type Renderer struct {
	Browser string
	Options Options
}

// New discovers a browser unless one is given.
func New(opts Options) (*Renderer, error) {
	browser := opts.Browser
	if browser == "" {
		browser = Default.Find()
	}
	if browser == "" {
		return nil, errors.New("chromium: no usable browser found (set CHROME_PATH, install Chromium, or use the font renderer)")
	}
	return &Renderer{Browser: browser, Options: opts}, nil
}

// HTML wraps a screen's cells in a document sized to the geometry.
//
// Every cell is absolutely positioned at its own pixel offset, so the layout does
// not depend on the font's advance at all: a per-cell inline-block layout overlapped
// its glyphs whenever the face was wider than the cell (the first version of this
// renderer drew "TnCd U" for "TinyCode TUI"), and a browser cannot be asked to
// behave like a terminal grid unless it is told where each cell is.
func (r *Renderer) HTML(b *screen.Buffer, cols, rows int) string {
	scale := r.Options.Scale
	if scale <= 0 {
		scale = 1
	}
	// The CSS box stays in unscaled pixels; --force-device-scale-factor produces the
	// extra pixels, so the geometry is written once.
	var doc strings.Builder
	fmt.Fprintf(&doc, `<!doctype html>
<html><head><meta charset="utf-8"><style>
  html, body { margin: 0; padding: 0; background: #1c1c1c; }
  #term { position: relative; width: %dpx; height: %dpx;
          font-family: monospace; font-size: %dpx; line-height: %dpx;
          white-space: pre; overflow: hidden; }
  .c { position: absolute; width: %dpx; height: %dpx;
       font-size: %dpx; line-height: %dpx; overflow: hidden; text-align: left; }
</style></head><body><div id="term">
`, cols*CellWidth, rows*CellHeight, CellHeight*3/4, CellHeight, CellWidth, CellHeight, CellHeight*3/4, CellHeight)

	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col++ {
			glyph, style, ok := b.CellAt(col, row)
			blank := !ok || glyph == 0
			if blank && style.BG == "" {
				continue
			}
			text := " "
			if !blank {
				text = string(glyph)
			}
			fmt.Fprintf(&doc, `<span class="c" style="left:%dpx;top:%dpx;%s">%s</span>`,
				col*CellWidth, row*CellHeight, cellCSS(style), html.EscapeString(text))
		}
	}
	doc.WriteString("</div></body></html>")
	return doc.String()
}

// cellCSS is the style declaration for one cell.
func cellCSS(style screen.Style) string {
	var css []string
	if style.FG != "" {
		css = append(css, "color:"+style.FG)
	}
	if style.BG != "" {
		css = append(css, "background:"+style.BG)
	}
	if style.Bold {
		css = append(css, "font-weight:700")
	}
	if style.Italic {
		css = append(css, "font-style:italic")
	}
	if style.Underline {
		css = append(css, "text-decoration:underline")
	}
	if style.Dim {
		css = append(css, "opacity:0.5")
	}
	return strings.Join(css, ";")
}

// PNG renders a screen through the browser and returns the image and its size.
func (r *Renderer) PNG(b *screen.Buffer, cols, rows int) ([]byte, int, int, error) {
	scale := r.Options.Scale
	if scale <= 0 {
		scale = 1
	}
	timeout := r.Options.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}

	dir, err := os.MkdirTemp("", "tuiprobe-shot")
	if err != nil {
		return nil, 0, 0, err
	}
	defer os.RemoveAll(dir)

	page := dir + "/page.html"
	if err := os.WriteFile(page, []byte(r.HTML(b, cols, rows)), 0o600); err != nil {
		return nil, 0, 0, err
	}
	shot := dir + "/shot.png"

	// The window is expressed in CSS pixels and the *device scale factor* produces
	// the extra pixels: setting both to scale would square it, which is how a 2x
	// capture first came back four times the size.
	windowW, windowH := cols*CellWidth, rows*CellHeight
	args := []string{
		"--headless",
		"--disable-gpu",
		"--hide-scrollbars",
		"--force-device-scale-factor=" + itoa(scale),
		"--window-size=" + itoa(windowW) + "," + itoa(windowH),
		"--screenshot=" + shot,
	}
	if shouldDisableSandbox() {
		args = append(args, "--no-sandbox", "--disable-dev-shm-usage")
	}
	args = append(args, r.Options.ExtraArgs...)
	args = append(args, "file://"+page)

	cmd := exec.Command(r.Browser, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, 0, 0, fmt.Errorf("chromium: start %s: %w", r.Browser, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return nil, 0, 0, fmt.Errorf("chromium: %s: %w: %s", shortName(r.Browser), err, lastLine(stderr.String()))
		}
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-done
		return nil, 0, 0, fmt.Errorf("chromium: %s did not finish within %s", shortName(r.Browser), timeout)
	}

	data, err := os.ReadFile(shot)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("chromium: no screenshot written: %w (%s)", err, lastLine(stderr.String()))
	}
	size, err := pngSize(data)
	if err != nil {
		return nil, 0, 0, err
	}

	minW, maxW, minH, maxH := Bounds(cols, rows, scale)
	if size.X < minW || size.X > maxW || size.Y < minH || size.Y > maxH {
		return nil, 0, 0, fmt.Errorf("chromium: screenshot is %dx%d, outside the %dx%d..%dx%d window a %dx%d screen allows",
			size.X, size.Y, minW, minH, maxW, maxH, cols, rows)
	}
	return data, size.X, size.Y, nil
}

// shouldDisableSandbox reports whether the browser needs to run without its own
// sandbox: as root (containers, CI images) it refuses to start otherwise.
func shouldDisableSandbox() bool {
	if os.Getenv("TUIPROBE_BROWSER_NO_SANDBOX") == "1" {
		return true
	}
	return os.Geteuid() == 0
}

// pngSize reads the IHDR dimensions without decoding the pixels.
func pngSize(data []byte) (image.Point, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return image.Point{}, fmt.Errorf("chromium: the screenshot is not a decodable image: %w", err)
	}
	return image.Pt(cfg.Width, cfg.Height), nil
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func shortName(path string) string {
	parts := strings.Split(path, "/")
	return parts[len(parts)-1]
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[len(lines)-1])
}
