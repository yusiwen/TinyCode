package tui

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/tool"
)

// Program-driver tests. The golden frames above render the model directly; they
// cannot see whether a real tea.Program ever paints it, whether a key reaches
// Update, or whether Run() returns. These tests drive the actual event loop
// with a pipe for input and a buffer for output, which needs no TTY and works
// in CI.
//
// Synchronization rules that make this race-free:
//   - Program.Send blocks until the event loop receives the message, and the
//     loop processes messages in order, so a later Send proves every earlier
//     one was processed.
//   - Model fields are only read after Run has returned.
//   - Rendered output is read through lockedBuffer.

// driverTimeout bounds every wait. A driver bug must fail the test, not hang CI.
const driverTimeout = 5 * time.Second

// lockedBuffer is a bytes.Buffer that can be read while the renderer writes.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// driverModel builds a model with hermetic dependencies: the session directory
// points into the test's temp dir, so quitting never writes outside it.
func driverModel(t *testing.T) *TuiModel {
	t.Helper()
	cfg := &config.Config{SessionDir: t.TempDir()}
	return NewTUI(agent.New(nil), cfg, agent.NewRegistry(),
		agent.NewProviderRegistry([]agent.ProviderRecord{
			{Name: "test", Provider: &agent.MockProvider{}},
		}), tool.NewTodoStore())
}

// programHarness runs a tea.Program against a pipe and a buffer.
type programHarness struct {
	t      *testing.T
	prog   *tea.Program
	keys   *io.PipeWriter
	out    *lockedBuffer
	model  *TuiModel
	done   chan struct{}
	err    error
	closed sync.Once
}

// startProgram launches the event loop and waits until the first frame has been
// painted, so a later Send cannot race the program's startup.
func startProgram(t *testing.T, m *TuiModel) *programHarness {
	t.Helper()
	pr, pw := io.Pipe()
	out := &lockedBuffer{}
	h := &programHarness{
		t:     t,
		keys:  pw,
		out:   out,
		model: m,
		done:  make(chan struct{}),
	}
	h.prog = tea.NewProgram(m,
		tea.WithInput(pr),
		tea.WithOutput(out),
		tea.WithoutSignalHandler(),
	)
	go func() {
		defer close(h.done)
		_, h.err = h.prog.Run()
	}()

	// The first paint is "Loading..." until a size arrives; waiting for it
	// proves Run() reached the renderer and the message channel exists.
	h.waitForOutput("Loading...")
	t.Cleanup(func() {
		h.closeKeys()
		select {
		case <-h.done:
		case <-time.After(driverTimeout):
			t.Errorf("program did not stop within %s", driverTimeout)
		}
	})
	return h
}

func (h *programHarness) closeKeys() {
	h.closed.Do(func() { h.keys.Close() })
}

// send hands a message to the event loop; it returns once the loop received it.
func (h *programHarness) send(msg tea.Msg) {
	h.t.Helper()
	h.prog.Send(msg)
}

// typeKeys writes raw key bytes to the program's input.
func (h *programHarness) typeKeys(s string) {
	h.t.Helper()
	if _, err := io.WriteString(h.keys, s); err != nil {
		h.t.Fatalf("write keys: %v", err)
	}
}

// waitForOutput polls the rendered stream until it contains want.
func (h *programHarness) waitForOutput(want string) {
	h.t.Helper()
	deadline := time.Now().Add(driverTimeout)
	for {
		if strings.Contains(h.out.String(), want) {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("rendered output never contained %q within %s\noutput so far:\n%s",
				want, driverTimeout, stripANSIView(h.out.String()))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// wait blocks until the event loop returns and reports its error.
func (h *programHarness) wait() error {
	h.t.Helper()
	select {
	case <-h.done:
		return h.err
	case <-time.After(driverTimeout):
		h.t.Fatalf("Run() did not return within %s", driverTimeout)
		return nil
	}
}

// TestProgramDriverPaintsAndQuits drives the real event loop end to end: it
// sizes the model, types into the input box, and quits with the documented
// double Ctrl+C.
func TestProgramDriverPaintsAndQuits(t *testing.T) {
	h := startProgram(t, driverModel(t))

	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.waitForOutput("Type your request")

	h.typeKeys("hello from the driver")
	h.waitForOutput("hello from the driver")

	h.typeKeys("\x03") // first Ctrl+C arms the quit confirmation
	// The bar truncates the hint at 80 columns, so match the part that survives.
	h.waitForOutput("Ctrl+C again")
	h.typeKeys("\x03") // second Ctrl+C quits

	if err := h.wait(); err != nil {
		t.Fatalf("Run() returned an error: %v", err)
	}
	if got := stripANSIView(h.out.String()); !strings.Contains(got, "hello from the driver") {
		t.Errorf("final frame lost the typed input:\n%s", got)
	}
	if got := h.model.input.Value(); got != "hello from the driver" {
		t.Errorf("input value after quit = %q, want the typed text", got)
	}
}

// TestProgramDriverResizeStorm hammers the resize path and proves the final
// geometry wins and nothing panics.
func TestProgramDriverResizeStorm(t *testing.T) {
	h := startProgram(t, driverModel(t))

	sizes := []tea.WindowSizeMsg{
		{Width: 80, Height: 24},
		{Width: 120, Height: 40},
		{Width: 200, Height: 50},
		{Width: 40, Height: 12},
		{Width: 100, Height: 30},
	}
	for i := 0; i < 25; i++ {
		h.send(sizes[i%len(sizes)])
	}
	last := sizes[24%len(sizes)]

	h.typeKeys("\x03")
	h.typeKeys("\x03")
	if err := h.wait(); err != nil {
		t.Fatalf("Run() returned an error after the resize storm: %v", err)
	}

	if h.model.width != last.Width || h.model.height != last.Height {
		t.Errorf("final geometry = %dx%d, want %dx%d",
			h.model.width, h.model.height, last.Width, last.Height)
	}
	if h.model.vp.Width != last.Width {
		t.Errorf("viewport width = %d, want %d (the grid must follow the terminal)",
			h.model.vp.Width, last.Width)
	}
}

// TestProgramDriverCommandQuits proves a typed command reaches the model's
// command handler through the real input path.
func TestProgramDriverCommandQuits(t *testing.T) {
	h := startProgram(t, driverModel(t))

	h.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.waitForOutput("Type your request")

	h.typeKeys("/exit")
	h.waitForOutput("/exit")
	h.typeKeys("\r")

	if err := h.wait(); err != nil {
		t.Fatalf("Run() returned an error after /exit: %v", err)
	}
}
