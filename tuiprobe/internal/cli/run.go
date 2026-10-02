package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/yusiwen/TinyCode/tuiprobe/golden"
	"github.com/yusiwen/TinyCode/tuiprobe/internal/daemon"
	"github.com/yusiwen/TinyCode/tuiprobe/internal/scenario"
	"github.com/yusiwen/TinyCode/tuiprobe/internal/shot"
	"github.com/yusiwen/TinyCode/tuiprobe/session"
)

// runScenario runs a scripted session, which is the reproducible form of a
// manual check and the shape CI wants.
func runScenario(args []string, stdout, stderr io.Writer) int {
	var update bool
	var gate, dir string
	cmd, err := newSessionCommand("run", args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.BoolVar(&update, "update", false, "rewrite goldens instead of comparing them")
		fs.StringVar(&gate, "gate", "", "skip unless this environment variable is set, and say so")
		fs.StringVar(&dir, "dir", "", "working directory for goldens and for the program")
	})
	if err != nil {
		return daemon.CodeFailure
	}
	if cmd.fs.NArg() != 1 {
		return fail(stderr, "run", "give one scenario file, for example: tuiprobe run tests/welcome.scenario")
	}

	file, err := os.Open(cmd.fs.Arg(0))
	if err != nil {
		return fail(stderr, "run", "%v", err)
	}
	defer file.Close()

	steps, err := scenario.Parse(file)
	if err != nil {
		return fail(stderr, "run", "%v", err)
	}

	// Progress goes to stderr so that --json on stdout stays parseable.
	result, runErr := scenario.Run(steps, scenario.Options{
		Socket: cmd.socket,
		Dir:    dir,
		Update: update,
		Gate:   gate,
		Name:   cmd.name,
		Stdout: stderr,
	})
	if runErr != nil {
		if cmd.asJSON {
			code := daemon.CodeFailure
			if errors.Is(runErr, session.ErrStageTimeout) {
				code = daemon.CodeTimeout
			}
			printJSON(stdout, daemon.Response{OK: false, Code: code, Error: runErr.Error()})
			return code
		}
		fmt.Fprintf(stderr, "tuiprobe run: %v\n", runErr)
		if errors.Is(runErr, session.ErrStageTimeout) {
			return daemon.CodeTimeout
		}
		return daemon.CodeFailure
	}

	if result.Skipped {
		if cmd.asJSON {
			printJSON(stdout, map[string]any{"ok": true, "skipped": true, "reason": result.Reason})
			return daemon.CodeOK
		}
		fmt.Fprintf(stderr, "skipped: %s\n", result.Reason)
		return daemon.CodeOK
	}
	if cmd.asJSON {
		printJSON(stdout, map[string]any{"ok": true, "steps": result.Steps})
		return daemon.CodeOK
	}
	fmt.Fprintf(stdout, "ok: %d steps\n", result.Steps)
	return daemon.CodeOK
}

// runDiff compares the current screen against a committed golden, which is the
// one-off version of what a scenario does in bulk.
func runDiff(args []string, stdout, stderr io.Writer) int {
	var against string
	var ansi, update bool
	cmd, err := newSessionCommand("diff", args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&against, "against", "", "the golden file to compare against")
		fs.BoolVar(&ansi, "ansi", false, "compare the ANSI screen instead of the plain text")
		fs.BoolVar(&update, "update", false, "rewrite the golden with what is on screen now")
	})
	if err != nil {
		return daemon.CodeFailure
	}
	if against == "" {
		return fail(stderr, "diff", "give --against <file>")
	}

	verb := "text"
	if ansi {
		verb = "ansi"
	}
	resp, code := cmd.call(daemon.Request{Cmd: verb})
	if code != daemon.CodeOK {
		return code
	}
	artifact := resp.Text
	if ansi {
		artifact = resp.ANSI
	} else {
		artifact = golden.Normalize(artifact)
	}

	if update {
		if err := golden.Write(filepath.Dir(against), filepath.Base(against), artifact); err != nil {
			return fail(stderr, "diff", "%v", err)
		}
		if cmd.asJSON {
			printJSON(stdout, map[string]any{"ok": true, "updated": against})
			return daemon.CodeOK
		}
		fmt.Fprintf(stdout, "updated %s\n", against)
		return daemon.CodeOK
	}

	if err := golden.Compare(filepath.Dir(against), filepath.Base(against), artifact); err != nil {
		if cmd.asJSON {
			printJSON(stdout, daemon.Response{OK: false, Code: daemon.CodeFailure, Error: err.Error()})
			return daemon.CodeFailure
		}
		fmt.Fprintf(stderr, "tuiprobe diff: %v\n", err)
		return daemon.CodeFailure
	}
	if cmd.asJSON {
		printJSON(stdout, map[string]any{"ok": true, "matches": against})
		return daemon.CodeOK
	}
	fmt.Fprintf(stdout, "matches %s\n", against)
	return daemon.CodeOK
}

// runShot captures the screen as a PNG (default) or as HTML.
func runShot(args []string, stdout, stderr io.Writer) int {
	var out, format, fontPath, renderer, browser string
	var scale int
	cmd, err := newSessionCommand("shot", args, stdout, stderr, func(fs *flag.FlagSet) {
		fs.StringVar(&out, "out", "", "file to write (the artifact's extension is the caller's business)")
		fs.StringVar(&format, "format", shot.FormatPNG, "png or html")
		fs.IntVar(&scale, "scale", 1, "enlarge the rendered text this many times")
		fs.StringVar(&fontPath, "font", "", "TTF/OTF to render with instead of the embedded face")
		fs.StringVar(&renderer, "renderer", shot.RendererFont, "font (no browser) or chromium (a real browser)")
		fs.StringVar(&browser, "browser", "", "browser to use for --renderer chromium; empty discovers one")
	})
	if err != nil {
		return daemon.CodeFailure
	}
	if out == "" {
		return fail(stderr, "shot", "give --out <file>")
	}

	resp, code := cmd.call(daemon.Request{Cmd: "ansi"})
	if code != daemon.CodeOK {
		return code
	}
	artifact, err := shot.Render(resp.ANSI, resp.Cols, resp.Rows, shot.Options{
		Format: format, Scale: scale, FontPath: fontPath, Renderer: renderer, Browser: browser,
	})
	if err != nil {
		return fail(stderr, "shot", "%v", err)
	}
	if err := shot.Write(out, artifact); err != nil {
		return fail(stderr, "shot", "%v", err)
	}

	if cmd.asJSON {
		printJSON(stdout, map[string]any{
			"ok": true, "file": out, "format": artifact.Format,
			"renderer": artifact.Renderer,
			"width":    artifact.Width, "height": artifact.Height,
			"cols": resp.Cols, "rows": resp.Rows,
		})
		return daemon.CodeOK
	}
	fmt.Fprintf(stdout, "%s %s %dx%d -> %s\n", artifact.Format, artifact.Renderer, artifact.Width, artifact.Height, out)
	return daemon.CodeOK
}
