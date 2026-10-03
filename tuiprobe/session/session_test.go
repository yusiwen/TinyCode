package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/TinyCode/tuiprobe/pty"
)

// TestHelperProcess is not a test: it is the fake TUI the session tests drive.
//
// A test needs a program that behaves like a TUI — paints a styled frame, reads
// keys, reacts to a resize and exits with a code — and re-executing this binary
// gives one that is always present and never depends on what is installed.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("TUIPROBE_HELPER") == "" {
		t.Skip("helper process: set TUIPROBE_HELPER=1 to run the fake TUI")
	}

	// A full-screen TUI puts its terminal into raw mode (term.MakeRaw /
	// tcsetattr); this helper uses stty so it needs no extra dependency. Without
	// it the terminal is line-buffered and a single keystroke is never delivered.
	raw := exec.Command("stty", "raw", "-echo")
	raw.Stdin = os.Stdin
	_ = raw.Run()

	out := os.Stdout
	fmt.Fprint(out, "\x1b[1;31mWELCOME\x1b[0m\r\nready\r\n")

	width, height := terminalSize()
	fmt.Fprintf(out, "size=%dx%d\r\n", width, height)

	buf := make([]byte, 1)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			switch buf[0] {
			case 'q':
				fmt.Fprint(out, "bye\r\n")
				os.Exit(3)
			case 'z':
				width, height = terminalSize()
				fmt.Fprintf(out, "size=%dx%d\r\n", width, height)
			case 'a':
				// A short animation: several repaints, so a caller has something
				// to wait to settle.
				for i := 0; i < 4; i++ {
					fmt.Fprintf(out, "\rstep %d", i)
					time.Sleep(15 * time.Millisecond)
				}
				fmt.Fprint(out, "\r\ndone\r\n")
			default:
				// Hex, not raw: an escape sequence sent back to a terminal is a
				// cursor movement, not text, so the test needs the bytes named.
				fmt.Fprintf(out, "key=%02x\r\n", buf[0])
			}
		}
		if err != nil {
			os.Exit(0)
		}
	}
}

// terminalSize asks the terminal for its geometry the way an application does.
// stdin must be the terminal itself: with no stdin attached, stty has nothing to
// ask and reports 0x0.
func terminalSize() (int, int) {
	cmd := exec.Command("stty", "size")
	cmd.Stdin = os.Stdin
	out, err := cmd.Output()
	if err != nil {
		return 0, 0
	}
	var rows, cols int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(out)), "%d %d", &rows, &cols); err != nil {
		return 0, 0
	}
	return cols, rows
}

// startHelper runs the fake TUI from this test binary.
func startHelper(t *testing.T) *Session {
	t.Helper()
	s, err := Start(Options{
		Args: []string{os.Args[0], "-test.run=TestHelperProcess"},
		Env:  []string{"TUIPROBE_HELPER=1"},
		Size: pty.Size{Cols: 80, Rows: 24},
	})
	if err != nil {
		t.Fatalf("start the helper: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSessionSeesTheScreenAndTheRawStream(t *testing.T) {
	s := startHelper(t)
	if err := s.WaitText("ready", 10*time.Second); err != nil {
		t.Fatalf("WaitText: %v", err)
	}

	if text := s.Text(); !strings.Contains(text, "WELCOME") {
		t.Errorf("screen text = %q, want it to contain WELCOME", text)
	}
	// The screen carries styling; the plain text does not.
	if ansi := s.ANSI(); !strings.Contains(ansi, "\x1b[0;1;31m") {
		t.Errorf("ANSI output lost the styling: %q", ansi)
	}
	if html := s.HTML(); !strings.Contains(html, "font-weight:700") {
		t.Errorf("HTML output lost the styling: %q", html)
	}
	// The trace keeps what the screen no longer shows.
	if trace := s.Trace(0); !strings.Contains(trace, "\x1b[") {
		t.Errorf("trace lost the raw escapes: %q", trace)
	}
}

func TestSessionSendsKeysAndGetsAFreshScreen(t *testing.T) {
	s := startHelper(t)
	if err := s.WaitText("ready", 10*time.Second); err != nil {
		t.Fatalf("WaitText: %v", err)
	}

	if err := s.Send("x"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	// The helper answers with the byte it read, in hex (0x78 is "x").
	if err := s.WaitText("key=78", 5*time.Second); err != nil {
		t.Fatalf("the program did not receive the key: %v", err)
	}

	// A named key reaches the program as its escape sequence, byte for byte:
	// up is ESC [ A, printed back as hex so the terminal cannot eat it.
	if err := s.Send("up", "enter"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.WaitText("key=1b\nkey=5b\nkey=41", 5*time.Second); err != nil {
		t.Fatalf("the arrow key did not arrive as an escape sequence: %v", err)
	}
}

func TestSessionWaitStableWaitsForTheAnimation(t *testing.T) {
	s := startHelper(t)
	if err := s.WaitText("ready", 10*time.Second); err != nil {
		t.Fatalf("WaitText: %v", err)
	}
	if err := s.Send("a"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	started := time.Now()
	if err := s.WaitStable(100*time.Millisecond, 10*time.Second); err != nil {
		t.Fatalf("WaitStable: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 100*time.Millisecond {
		t.Errorf("WaitStable returned after %s, before the quiet window elapsed", elapsed)
	}
	if text := s.Text(); !strings.Contains(text, "done") {
		t.Errorf("the screen settled without the animation's last frame: %q", text)
	}
}

func TestSessionWaitsAreBoundedAndExplainThemselves(t *testing.T) {
	s := startHelper(t)
	if err := s.WaitText("ready", 10*time.Second); err != nil {
		t.Fatalf("WaitText: %v", err)
	}

	started := time.Now()
	err := s.WaitText("never appears", 200*time.Millisecond)
	if err == nil {
		t.Fatal("waiting for absent text must fail")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("the wait took %s, want it bounded by the 200ms timeout", elapsed)
	}
	// The failure must name the step, the pattern and what the screen said.
	for _, want := range []string{"wait for text", "never appears", "WELCOME"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestSessionReportsTheExitCode(t *testing.T) {
	s := startHelper(t)
	if err := s.WaitText("ready", 10*time.Second); err != nil {
		t.Fatalf("WaitText: %v", err)
	}
	if err := s.Send("q"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.WaitText("bye", 5*time.Second); err != nil {
		t.Fatalf("the program did not say goodbye: %v", err)
	}
	code, err := s.Wait()
	if code != 3 || err == nil {
		t.Errorf("Wait = (%d, %v), want exit code 3 and the non-zero status", code, err)
	}
}

func TestSessionResizeRestartsTheScreenAtTheNewGeometry(t *testing.T) {
	s := startHelper(t)
	if err := s.WaitText("ready", 10*time.Second); err != nil {
		t.Fatalf("WaitText: %v", err)
	}
	if err := s.Resize(pty.Size{Cols: 100, Rows: 30}); err != nil {
		t.Fatalf("Resize: %v", err)
	}
	if got := s.Size(); got.Cols != 100 || got.Rows != 30 {
		t.Errorf("Size = %+v, want 100x30", got)
	}
	// The program is told too: ask it what it sees now.
	if err := s.Send("z"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := s.WaitText("100x30", 5*time.Second); err != nil {
		t.Fatalf("the program did not see the new geometry: %v", err)
	}
}

func TestKeyEncoding(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{"enter", "\r"},
		{"ctrl+c", "\x03"},
		{"alt+left", "\x1b\x1b[D"},
		{"shift+tab", "\x1b[Z"},
		{"f5", "\x1b[15~"},
		{"x", "x"},
	} {
		got, ok := Key(tc.name)
		if !ok || string(got) != tc.want {
			t.Errorf("Key(%q) = (%q, %v), want %q", tc.name, got, ok, tc.want)
		}
	}
	if _, ok := Key("not-a-key"); ok {
		t.Error("a multi-character non-key name must not resolve to a key")
	}
	if _, ok := Key("ctrl+1"); ok {
		t.Error("ctrl+<digit> is not a control byte and must not resolve")
	}
	if got, err := Repeat("up", 2); err != nil || string(got) != "\x1b[A\x1b[A" {
		t.Errorf("Repeat(up, 2) = (%q, %v)", got, err)
	}
}

// TestStageNamesTheStepThatRanOut covers the rule "bound and name every stage":
// a step that overruns its budget fails with the step's name, and the error is
// recognisable as a timeout without parsing the text.
func TestStageNamesTheStepThatRanOut(t *testing.T) {
	started := time.Now()
	err := Stage("wait-for-banner", 80*time.Millisecond, func() error {
		time.Sleep(2 * time.Second) // ignores its budget on purpose
		return nil
	})
	if err == nil {
		t.Fatal("a stage that overran its budget must fail")
	}
	if !errors.Is(err, ErrStageTimeout) {
		t.Errorf("error %v is not a stage timeout", err)
	}
	var stageErr *StageError
	if !errors.As(err, &stageErr) || stageErr.Name != "wait-for-banner" || !stageErr.Timeout {
		t.Fatalf("error = %#v, want a named timeout for wait-for-banner", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("the stage returned after %s, want it bounded by its 80ms budget", elapsed)
	}

	// A failing step keeps the stage name and the underlying error.
	sentinel := errors.New("boom")
	err = Stage("send-keys", time.Second, func() error { return sentinel })
	if !errors.Is(err, sentinel) {
		t.Errorf("error %v does not wrap the step's error", err)
	}
	if !strings.Contains(err.Error(), "send-keys") {
		t.Errorf("error %q does not name the stage", err)
	}
	if _, ok := err.(*StageError); !ok {
		t.Errorf("error %T is not a *StageError", err)
	}
}

// TestStageTextUsesTheSessionBudget checks the two helpers a scenario step uses.
func TestStageTextUsesTheSessionBudget(t *testing.T) {
	s := startHelper(t)
	if err := s.StageText("banner", "ready", 10*time.Second); err != nil {
		t.Fatalf("StageText: %v", err)
	}
	if err := s.StageText("missing", "never appears", 150*time.Millisecond); !errors.Is(err, ErrStageTimeout) {
		t.Errorf("waiting for absent text = %v, want a stage timeout", err)
	}
	if err := s.StageStable("settle", 50*time.Millisecond, 5*time.Second); err != nil {
		t.Errorf("StageStable: %v", err)
	}
}

// TestWaitTextSucceedsForAProgramThatHasAlreadyExited: a program that prints its
// answer and leaves has satisfied the wait. The exit flag and the final screen are not
// ordered — the reader may still be applying the last bytes — so the wait re-checks
// briefly instead of failing. A CI run of the CLI tests found "got:hello" on screen
// together with "the program exited first".
func TestWaitTextSucceedsForAProgramThatHasAlreadyExited(t *testing.T) {
	s, err := Start(Options{Args: []string{"/bin/sh", "-c", `printf 'printed\n'`}, Size: pty.Size{Cols: 40, Rows: 4}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()
	if err := s.WaitText("printed", 5*time.Second); err != nil {
		t.Errorf("a program that printed and exited must satisfy the wait: %v", err)
	}

	// A stable wait after the exit is satisfied by definition: nothing can change.
	s2, err := Start(Options{Args: []string{"/bin/sh", "-c", `printf 'gone\n'`}, Size: pty.Size{Cols: 40, Rows: 4}})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	time.Sleep(50 * time.Millisecond)
	if err := s2.WaitStable(200*time.Millisecond, 5*time.Second); err != nil {
		t.Errorf("a stable screen after the exit must pass: %v", err)
	}
}

// TestTerminalQueriesAreAnswered is issue #76: TinyCode writes OSC 11 and CSI 6n at
// startup and paints nothing until they time out (~5s measured), because a real
// terminal answers both. The replies are asserted as the terminal echoes them, which
// is what proves the session wrote them; the switch is asserted by its absence.
func TestTerminalQueriesAreAnswered(t *testing.T) {
	// The program asks both questions and then just sits there.
	script := `printf '\033]11;?\033\\'; printf '\033[6n'; sleep 10`
	s, err := Start(Options{
		Args:       []string{"/bin/sh", "-c", script},
		Size:       pty.Size{Cols: 40, Rows: 4},
		Background: "#123456",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Close()

	// OSC 11: four hex digits per channel, the colour the session was configured with.
	if err := s.WaitText(`rgb:1212/3434/5656`, 10*time.Second); err != nil {
		t.Fatalf("OSC 11 was not answered: %v\nscreen:\n%s", err, s.Text())
	}
	// CSI 6n: row;column, one-based, taken from the session's own cursor.
	if !regexp.MustCompile(`\[1;[0-9]+R`).MatchString(s.Text()) {
		t.Errorf("CSI 6n was not answered with a cursor position:\n%s", s.Text())
	}

	// The switch exists so the opposite can be proven: a program that must cope with
	// a silent terminal gets one. Absence is the assertion, so it is a bounded wait
	// that must time out.
	silent, err := Start(Options{
		Args:           []string{"/bin/sh", "-c", script},
		Size:           pty.Size{Cols: 40, Rows: 4},
		NoQueryAnswers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	if err := silent.WaitText(`rgb:`, 2*time.Second); err == nil {
		t.Errorf("with answers off the session still replied:\n%s", silent.Text())
	}
}
