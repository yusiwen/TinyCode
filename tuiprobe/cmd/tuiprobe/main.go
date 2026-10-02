// Command tuiprobe is the standalone binary of the tuiprobe library: it drives
// any TUI, on any platform, and turns what the program really drew into
// artifacts a person, a test or an agent can read.
//
// The library it wraps is importable — github.com/yusiwen/TinyCode/tuiprobe —
// so a Go project can use the same engine in-process instead of shelling out.
package main

import (
	"os"

	"github.com/yusiwen/TinyCode/tuiprobe/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
