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
	case "open":
		return runOpen(args[1:], stdout, stderr)
	case "send":
		return runSend(args[1:], stdout, stderr)
	case "wait":
		return runWait(args[1:], stdout, stderr)
	case "text", "ansi", "html", "trace":
		return runScreen(args[0], args[1:], stdout, stderr)
	case "resize":
		return runResize(args[1:], stdout, stderr)
	case "close":
		return runClose(args[1:], stdout, stderr)
	case "wait-exit":
		return runWaitExit(args[1:], stdout, stderr)
	case "sessions":
		return runSessions(args[1:], stdout, stderr)
	case "daemon":
		return runDaemon(args[1:], stdout, stderr)
	case "run":
		return runScenario(args[1:], stdout, stderr)
	case "diff":
		return runDiff(args[1:], stdout, stderr)
	case "shot":
		return runShot(args[1:], stdout, stderr)
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

Sessions (a program on a real terminal, kept alive between commands):
  open [--name app] [--size 80x24] [--dir d] [--env K=V]… -- <command> [args…]
  send --name app [--text "…"] [--key enter] [--key ctrl+c] [--repeat n]
  wait --name app [--text <regexp>] [--stable 200ms] [--timeout 10s]
  text|ansi|html --name app        print the screen
  trace --name app [--n 4096]      print the tail of the raw stream
  resize --name app --size 100x30
  close --name app [--expect-exit n]
  wait-exit --name app [--timeout 10s]   wait for the program to end on its own
  diff --name app --against <file> [--ansi] [--update]
  shot --name app --out app.png [--format png|html] [--scale 2] [--font path.ttf]
  sessions                         list live sessions

Scripted:
  run [--update] [--gate VAR] [--dir DIR] <scenario>   steps: open, send, wait,
      stable, sleep, golden, diff, fit, close, expect-exit

Offline:
  replay --stream <file|-> --size 80x24 [--format text|ansi|html]
  version | help

Every session command takes --json and --socket. The socket defaults to
$TUIPROBE_SOCKET, else a per-user path in $TMPDIR; the first command starts the
daemon, which exits on its own once idle.

Exit codes:
  0 success · 2 usage or assertion failure · 3 timeout · 4 unknown session
`)
}
