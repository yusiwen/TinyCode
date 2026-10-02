// Package cli is the command-line front end of tuiprobe, kept in its own package
// so the commands can be tested in-process — a CLI that is only ever exercised
// by building a binary and running it is a CLI nobody tests.
package cli

import (
	"fmt"
	"io"
	"runtime"
)

// Version is the tool version, overridable at build time:
//
//	go build -ldflags '-X github.com/yusiwen/TinyCode/tuiprobe/internal/cli.Version=v0.1.0'
var Version = "dev"

// Run dispatches one invocation and returns the process exit code:
//
//	0 success · 2 usage or assertion failure · 3 timeout · 4 unknown session
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "tuiprobe %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		return 0
	case "help", "--help", "-h":
		usage(stdout)
		return 0
	case "replay":
		return runReplay(args[1:], stdin, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "tuiprobe: unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `tuiprobe — see and assert what a TUI really draws

Usage:
  tuiprobe <command> [flags]

Commands:
  replay     replay a captured terminal stream through the emulator and print
             the screen as text, ANSI or HTML
  version    print the version
  help       print this help

Exit codes:
  0 success · 2 usage or assertion failure · 3 timeout · 4 unknown session

The session commands (open, send, wait, text, ansi, shot, close, sessions) and
the scenario runner are in progress; see docs/roadmap.md.
`)
}
