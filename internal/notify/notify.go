// Package notify announces that something finished.
//
// It exists for one gap (roadmap D5): a background task or a long run ends and
// nothing says so. The mechanism is deliberately small — a terminal escape by
// default, the platform's own notifier when asked — and the policy is the part
// worth arguing about, so it is all in Config: a channel, and how long something
// has to take before it is worth interrupting anyone over.
package notify

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Channel is how a completion announces itself.
type Channel string

const (
	// ChannelTerminal writes the escapes a terminal turns into a notification. It
	// is the default: invisible unless the terminal supports it, needs no
	// external process, and makes no sound.
	ChannelTerminal Channel = "terminal"
	// ChannelDesktop runs the platform's own notifier. Opt-in, because a
	// notification-centre entry per long run is a different kind of intrusion
	// from a terminal that quietly shows nothing.
	ChannelDesktop Channel = "desktop"
	// ChannelBoth does both.
	ChannelBoth Channel = "both"
	// ChannelOff announces nothing, whatever the elapsed time.
	ChannelOff Channel = "off"
)

// DefaultMinDuration is how long something has to take before it is worth an
// announcement. It is an approximation of "you have probably looked away": there
// is no portable way to ask whether a terminal has focus, and a per-terminal
// hack for it would be worse than a threshold.
const DefaultMinDuration = 30 * time.Second

// commandTimeout bounds a platform notifier. It is short because a notification
// is never worth blocking a finished run on, and a notifier that hangs is a bug
// in the notifier.
const commandTimeout = 3 * time.Second

// errUnsupported is returned for a platform this package cannot announce on.
var errUnsupported = errors.New("no desktop notifier for this platform")

// Config is the policy: which channel, and the shortest thing worth announcing.
type Config struct {
	Channel     Channel
	MinDuration time.Duration
}

// DefaultConfig is the built-in policy: a terminal notification after 30 seconds.
// Enabled rather than off, because a terminal notification is invisible unless
// the terminal supports it — so it works out of the box without interrupting
// anyone who has not asked for more.
func DefaultConfig() Config {
	return Config{Channel: ChannelTerminal, MinDuration: DefaultMinDuration}
}

// Normalize fills in what a caller left out and repairs a channel it cannot read.
// An unknown value becomes the default rather than ChannelOff: a typo in a
// configuration file should not silently switch a feature the user asked for off.
func Normalize(c Config) Config {
	switch c.Channel {
	case ChannelTerminal, ChannelDesktop, ChannelBoth:
	case ChannelOff:
		// Off is a real choice, and it stays one.
	default:
		c.Channel = DefaultConfig().Channel
	}
	if c.MinDuration < 0 {
		c.MinDuration = DefaultMinDuration
	}
	return c
}

var (
	mu       sync.Mutex
	config             = DefaultConfig()
	sink     io.Writer = os.Stdout
	runCmd             = execCommand
	lookPath           = exec.LookPath
)

// Configure installs the policy. It is called once at startup, the way
// tool.SetSearXNG is.
func Configure(c Config) {
	mu.Lock()
	defer mu.Unlock()
	config = Normalize(c)
}

// SetSink replaces where a terminal notification is written. It is the seam a
// test asserts exact bytes through; the TUI leaves it at os.Stdout.
func SetSink(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	sink = w
}

// SetCommandRunner replaces how a desktop notifier is run, so a test can assert
// which command would have run without spawning one.
func SetCommandRunner(fn func(name string, args ...string) error) {
	mu.Lock()
	defer mu.Unlock()
	runCmd = fn
}

// SetLookPath replaces the binary lookup, so a test can present a machine where
// the notifier is not installed.
func SetLookPath(fn func(string) (string, error)) {
	mu.Lock()
	defer mu.Unlock()
	lookPath = fn
}

// Reset restores the built-in policy and the real sink and runner.
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	config = DefaultConfig()
	sink = os.Stdout
	runCmd = execCommand
	lookPath = exec.LookPath
}

// Sequence is the terminal's share of a notification: OSC 9, which iTerm2,
// WezTerm, kitty and Ghostty turn into a notification, and OSC 777, which
// rxvt-unicode does. Both are sent because a terminal ignores an OSC it does not
// recognise — the same bet the CellGrid makes with OSC 8 hyperlinks.
//
// The text is sanitized first: a control byte in it would be a way for a task's
// own goal text to end the escape and write to the terminal instead, so ESC, BEL
// and line breaks never survive, and a ";" in the title cannot shift OSC 777's
// fields (the body is last, so a ";" there is harmless and is kept).
func Sequence(title, body string) string {
	title = sanitizeField(title, true)
	body = sanitizeField(body, false)
	return "\x1b]9;" + body + "\x07" + "\x1b]777;notify;" + title + ";" + body + "\x07"
}

// sanitizeField removes every byte that could terminate or reshape an escape.
// field says whether the ";" separator matters for this position.
func sanitizeField(s string, field bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r < 0x20 || r == 0x7f: // ESC, BEL, newline, tab, and the rest
			continue
		case field && r == ';':
			b.WriteRune(',')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Finished announces that something took elapsed and is over. It reports whether
// anything was emitted: a run below the configured minimum is deliberately
// silent, and so is ChannelOff.
//
// It runs the platform notifier synchronously when the channel asks for one, so a
// caller on an event loop must call it from its own goroutine or command — the
// bound is commandTimeout.
func Finished(elapsed time.Duration, title, body string) bool {
	mu.Lock()
	cfg, w, run, look := config, sink, runCmd, lookPath
	mu.Unlock()

	cfg = Normalize(cfg)
	if cfg.Channel == ChannelOff {
		return false
	}
	if cfg.MinDuration > 0 && elapsed < cfg.MinDuration {
		return false
	}

	emitted := false
	if cfg.Channel == ChannelTerminal || cfg.Channel == ChannelBoth {
		if w != nil {
			// A write error to a terminal is not worth reporting: the caller has
			// already finished its work, and it has nothing to do about it.
			if _, err := io.WriteString(w, Sequence(title, body)); err == nil {
				emitted = true
			}
		}
	}
	if cfg.Channel == ChannelDesktop || cfg.Channel == ChannelBoth {
		if err := desktopNotify(title, body, run, look); err == nil {
			emitted = true
		}
	}
	return emitted
}

// desktopNotify runs the platform's own notifier.
//
// The text is passed as arguments, never interpolated into a script, so a goal
// containing a quote cannot change what runs. A platform with no notifier here, or
// a machine where the binary is missing, returns an error the caller ignores: the
// run it was announcing has already finished successfully.
func desktopNotify(title, body string, run func(string, ...string) error, look func(string) (string, error)) error {
	switch runtime.GOOS {
	case "darwin":
		if _, err := look("osascript"); err != nil {
			return err
		}
		return run("osascript",
			"-e", "on run argv",
			"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
			"-e", "end run",
			title, body)
	case "linux":
		if _, err := look("notify-send"); err != nil {
			return err
		}
		return run("notify-send", title, body)
	default:
		return errUnsupported
	}
}

// execCommand runs a notifier under a bound, discarding its output.
func execCommand(name string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr, cmd.Stdin = nil, nil, nil
	return cmd.Run()
}
