package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/yusiwen/TinyCode/tuiprobe/screen"
)

// replayOptions are the flags of `tuiprobe replay`.
type replayOptions struct {
	stream string
	size   string
	format string
}

// runReplay reads a captured terminal stream and prints the screen it produces.
//
// This is the smallest useful command of the tool and the one that proves the
// emulator end to end without a process: a stream recorded from any TUI (a
// `script` capture, a saved PTY log, the trace of a session) becomes a screen
// that can be read, diffed or asserted on.
func runReplay(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := replayOptions{}
	fs.StringVar(&opts.stream, "stream", "-", `stream file to replay, "-" for stdin`)
	fs.StringVar(&opts.size, "size", "80x24", "terminal size the stream was produced for, as WxH")
	fs.StringVar(&opts.format, "format", "text", "output format: text, ansi or html")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	width, height, err := parseSize(opts.size)
	if err != nil {
		fmt.Fprintf(stderr, "tuiprobe replay: %v\n", err)
		return 2
	}

	var data []byte
	if opts.stream == "-" {
		data, err = io.ReadAll(stdin)
	} else {
		data, err = os.ReadFile(opts.stream)
	}
	if err != nil {
		fmt.Fprintf(stderr, "tuiprobe replay: read stream: %v\n", err)
		return 2
	}

	terminal := screen.New(width, height)
	if _, err := terminal.Write(data); err != nil {
		fmt.Fprintf(stderr, "tuiprobe replay: feed stream: %v\n", err)
		return 2
	}

	switch opts.format {
	case "text", "":
		fmt.Fprint(stdout, terminal.String())
	case "ansi":
		fmt.Fprint(stdout, terminal.Render())
	case "html":
		fmt.Fprint(stdout, terminal.HTML())
	default:
		fmt.Fprintf(stderr, "tuiprobe replay: unknown format %q (want text, ansi or html)\n", opts.format)
		return 2
	}
	return 0
}

// parseSize reads a WxH geometry.
func parseSize(s string) (int, int, error) {
	w, h, ok := strings.Cut(strings.ToLower(s), "x")
	if !ok {
		return 0, 0, fmt.Errorf("size %q is not WxH (for example 80x24)", s)
	}
	width, err := strconv.Atoi(w)
	if err != nil || width <= 0 {
		return 0, 0, fmt.Errorf("size %q has a bad width", s)
	}
	height, err := strconv.Atoi(h)
	if err != nil || height <= 0 {
		return 0, 0, fmt.Errorf("size %q has a bad height", s)
	}
	return width, height, nil
}
