package bubbletea

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// counter is the smallest real program: it paints, it reacts to keys, it reports its
// window, and it quits on "q" — the four things the harness's driver tests needed.
type counter struct {
	count    int
	width    int
	height   int
	quitting bool
}

func (m counter) Init() tea.Cmd { return nil }

func (m counter) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q":
			m.quitting = true
			return m, tea.Quit
		case "up":
			m.count++
		}
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case customMsg:
		m.count += 10
	}
	return m, nil
}

func (m counter) View() string {
	if m.quitting {
		return "bye\n"
	}
	return "count=" + itoa(m.count) + " size=" + itoa(m.width) + "x" + itoa(m.height) + "\n"
}

type customMsg struct{}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func key(s string) tea.KeyMsg {
	switch s {
	case "q":
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

// TestProgramPaintsAndQuits is the harness case "paints and quits": the model's View
// must reach the screen, and "q" must end the run.
func TestProgramPaintsAndQuits(t *testing.T) {
	p, err := Start(counter{}, Options{Cols: 40, Rows: 6})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := p.WaitText("count=0 size=40x6", 5*time.Second); err != nil {
		t.Fatalf("WaitText: %v\nscreen:\n%s", err, p.Text())
	}
	if !strings.Contains(p.Text(), "size=40x6") {
		t.Errorf("the model never saw its window:\n%s", p.Text())
	}

	if err := p.Send(key("q")); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := p.WaitText("bye", time.Second); err != nil {
		t.Errorf("the model's farewell did not reach the screen: %v\nscreen:\n%s", err, p.Text())
	}
}

// TestProgramDrivesCommandsAndKeys: a message sent from the outside (a command's
// result) must reach Update just like a keystroke.
func TestProgramDrivesCommandsAndKeys(t *testing.T) {
	p, err := Start(counter{}, Options{Cols: 40, Rows: 6})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Quit() // a program that is never asked to quit makes Wait block forever

	if err := p.Send(key("up")); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitText("count=1", 5*time.Second); err != nil {
		t.Fatalf("a keystroke did not reach Update: %v", err)
	}
	if err := p.Send(customMsg{}); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitText("count=11", 5*time.Second); err != nil {
		t.Fatalf("a message did not reach Update: %v", err)
	}
}

// TestProgramResizeStorm is the harness case of the same name: many resizes in a row
// must leave the program painting for the last geometry, not for one in the middle.
func TestProgramResizeStorm(t *testing.T) {
	p, err := Start(counter{}, Options{Cols: 40, Rows: 6})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Quit() // a program that is never asked to quit makes Wait block forever
	if err := p.WaitText("count=0 size=40x6", 5*time.Second); err != nil {
		t.Fatal(err)
	}

	for _, size := range [][2]int{{60, 10}, {30, 8}, {100, 20}, {45, 12}} {
		if err := p.Resize(size[0], size[1]); err != nil {
			t.Fatalf("Resize: %v", err)
		}
	}
	if err := p.WaitText("size=45x12", 5*time.Second); err != nil {
		t.Fatalf("after the storm the program is not painting the last geometry: %v\nscreen:\n%s", err, p.Text())
	}
}

// TestProgramZeroSizeWindow is parity row 10's in-process half: a model handed a
// window that reports nothing must not be handed a 0x0 screen, because the models
// that divide by width crash on one.
func TestProgramZeroSizeWindow(t *testing.T) {
	p, err := Start(counter{}, Options{Cols: 0, Rows: 0})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Quit() // a program that is never asked to quit makes Wait block forever

	cols, rows := p.Size()
	if cols != DefaultSize.Cols || rows != DefaultSize.Rows {
		t.Errorf("Size = %dx%d, want the default %dx%d", cols, rows, DefaultSize.Cols, DefaultSize.Rows)
	}
	if err := p.WaitText("size=80x24", 5*time.Second); err != nil {
		t.Fatalf("the model did not see a usable window: %v\nscreen:\n%s", err, p.Text())
	}

	// And a resize to nothing is upgraded too.
	if err := p.Resize(0, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.Send(key("up")); err != nil {
		t.Fatal(err)
	}
	if err := p.WaitText("size=80x24", 5*time.Second); err != nil {
		t.Fatalf("a zero resize was not upgraded: %v", err)
	}
}

// TestOutputIsAScreenNotAString: Text() must come from the emulator, so cursor
// movement and erases are interpreted rather than printed.
func TestOutputIsAScreenNotAString(t *testing.T) {
	p, err := Start(counter{}, Options{Cols: 30, Rows: 4})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Quit() // a program that is never asked to quit makes Wait block forever
	if err := p.WaitText("count=0", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := p.Send(key("up")); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.WaitText("count=3", 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// The raw stream carries the escape sequences; the screen does not.
	if !strings.Contains(p.Raw(), "\x1b[") {
		t.Error("the raw output has no escapes; the renderer is not painting a terminal")
	}
	if strings.Contains(p.Text(), "\x1b[") {
		t.Error("the screen still contains escape sequences; it was not emulated")
	}
	if got := p.Text(); !strings.Contains(got, "count=3") || strings.Contains(got, "count=2") {
		t.Errorf("the screen shows stale frames:\n%s", got)
	}
}
