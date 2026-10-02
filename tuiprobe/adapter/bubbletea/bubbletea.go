// Package bubbletea drives a Bubble Tea program in-process.
//
// It is the adapter for the case a PTY cannot serve: a Go test that wants the real
// program — its update loop, its commands, its renderer — without spawning a binary.
// The harness had exactly this (four tests: paints and quits, a resize storm, a
// command that quits, and a window that reports no size), and it is worth keeping:
// the PTY path proves the program as a black box, this one proves the program as a
// Go value.
//
// The output is fed through the same terminal emulator the PTY path uses, so
// `Text()` here means the same thing as `Text()` there: what a user would see, not
// what a function returned.
package bubbletea

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/yusiwen/TinyCode/tuiprobe/screen"
)

// DefaultSize is the geometry a program gets when the caller does not choose one:
// a terminal program that reads a 0x0 window divides by zero.
var DefaultSize = struct{ Cols, Rows int }{Cols: 80, Rows: 24}

// PollInterval bounds how often the wait helpers look at the screen.
var PollInterval = 20 * time.Millisecond

// Options configure a program.
type Options struct {
	Cols, Rows int
	// Extra are additional Bubble Tea options, e.g. an alternate renderer or an
	// input filter. Environment variables are the process's, as they are for a
	// program run on a terminal.
	Extra []tea.ProgramOption
}

// Program is a running Bubble Tea program whose output is a screen.
type Program struct {
	model tea.Model
	prog  *tea.Program

	mu       sync.Mutex
	raw      strings.Builder
	terminal *screen.Buffer
	size     struct{ Cols, Rows int }

	done chan struct{}
	err  error
}

// Start runs a model with piped input and output.
func Start(model tea.Model, opts Options) (*Program, error) {
	if model == nil {
		return nil, fmt.Errorf("bubbletea: no model")
	}
	size := DefaultSize
	if opts.Cols > 0 && opts.Rows > 0 {
		size = struct{ Cols, Rows int }{Cols: opts.Cols, Rows: opts.Rows}
	}

	in, inWriter := io.Pipe()
	options := []tea.ProgramOption{
		tea.WithInput(in),
		tea.WithOutput(&pipeline{p: nil}), // replaced below
		tea.WithoutSignalHandler(),
	}
	p := &Program{
		model:    model,
		terminal: screen.New(size.Cols, size.Rows),
		size:     size,
		done:     make(chan struct{}),
	}
	out := &pipeline{p: p}
	options[1] = tea.WithOutput(out)
	options = append(options, opts.Extra...)

	p.prog = tea.NewProgram(model, options...)

	go func() {
		_, err := p.prog.Run()
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		close(p.done)
		_ = inWriter.Close()
	}()

	// A program only learns its geometry from a message, exactly as a terminal would
	// send it; give it one up front so a model that reads Width in View() sees a
	// terminal rather than zero.
	p.prog.Send(tea.WindowSizeMsg{Width: size.Cols, Height: size.Rows})
	return p, nil
}

// pipeline is the io.Writer the program paints into: every byte goes to the raw
// buffer and through the emulator.
type pipeline struct{ p *Program }

func (w *pipeline) Write(b []byte) (int, error) {
	w.p.mu.Lock()
	defer w.p.mu.Unlock()
	w.p.raw.Write(b)
	_, _ = w.p.terminal.Write(b)
	return len(b), nil
}

// Send delivers a message to the update loop.
func (p *Program) Send(msg tea.Msg) error {
	p.prog.Send(msg)
	return nil
}

// Resize tells the program its window changed, which is how a terminal reports it.
func (p *Program) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		cols, rows = DefaultSize.Cols, DefaultSize.Rows
	}
	p.mu.Lock()
	p.size = struct{ Cols, Rows int }{Cols: cols, Rows: rows}
	p.terminal = screen.New(cols, rows)
	// Keep what has been painted: the program will repaint on the next message, and
	// the old painting is not evidence about the new geometry.
	p.raw.Reset()
	p.mu.Unlock()
	p.prog.Send(tea.WindowSizeMsg{Width: cols, Height: rows})
	return nil
}

// Size is the current geometry.
func (p *Program) Size() (cols, rows int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.size.Cols, p.size.Rows
}

// Text is the screen as plain text.
func (p *Program) Text() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminal.String()
}

// ANSI is the screen with its styling.
func (p *Program) ANSI() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminal.Render()
}

// HTML is the screen as styled markup.
func (p *Program) HTML() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.terminal.HTML()
}

// Raw is everything the program has painted, escapes included: the frame as the
// renderer emitted it, which is what a frame golden compares.
func (p *Program) Raw() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.raw.String()
}

// WaitText waits until the screen matches a pattern.
func (p *Program) WaitText(pattern string, timeout time.Duration) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("bubbletea: bad pattern %q: %w", pattern, err)
	}
	deadline := time.Now().Add(timeout)
	for {
		if re.MatchString(p.Text()) {
			return nil
		}
		if p.finished() {
			return fmt.Errorf("bubbletea: wait for text %q: the program exited first; screen:\n%s", pattern, p.Text())
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("bubbletea: wait for text %q: timed out after %s; screen:\n%s", pattern, timeout, p.Text())
		}
		time.Sleep(PollInterval)
	}
}

// WaitStable waits until the program has painted nothing for quiet.
func (p *Program) WaitStable(quiet, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastLen := -1
	var lastChange time.Time
	for {
		p.mu.Lock()
		length := p.raw.Len()
		p.mu.Unlock()
		if length != lastLen {
			lastLen, lastChange = length, time.Now()
		} else if time.Since(lastChange) >= quiet {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("bubbletea: the program kept painting for %s; screen:\n%s", timeout, p.Text())
		}
		time.Sleep(PollInterval)
	}
}

// Quit asks the program to stop.
func (p *Program) Quit() error {
	p.prog.Quit()
	return p.Wait()
}

// Wait blocks until the program exits and returns the error from Run.
func (p *Program) Wait() error {
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Finished reports whether the program has exited.
func (p *Program) Finished() bool { return p.finished() }

func (p *Program) finished() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}
