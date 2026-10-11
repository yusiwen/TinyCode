package notify

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// recorder is the two seams a test needs: what would be written, and what would
// have run. Nothing here spawns a process.
type recorder struct {
	out      bytes.Buffer
	commands [][]string
	lookErr  error
}

func (r *recorder) install(t *testing.T) {
	t.Helper()
	Reset()
	t.Cleanup(Reset)
	SetSink(&r.out)
	SetCommandRunner(func(name string, args ...string) error {
		r.commands = append(r.commands, append([]string{name}, args...))
		return nil
	})
	SetLookPath(func(string) (string, error) {
		if r.lookErr != nil {
			return "", r.lookErr
		}
		return "/usr/bin/notifier", nil
	})
}

// TestSequenceShape pins the two escapes and their terminators.
func TestSequenceShape(t *testing.T) {
	got := Sequence("TinyCode", "background task finished")
	want := "\x1b]9;background task finished\x07" +
		"\x1b]777;notify;TinyCode;background task finished\x07"
	if got != want {
		t.Errorf("Sequence = %q, want %q", got, want)
	}
	if n := strings.Count(got, "\x07"); n != 2 {
		t.Errorf("BEL count = %d, want 2 (one per escape)", n)
	}
	if !strings.HasPrefix(got, "\x1b]9;") {
		t.Error("the OSC 9 escape should come first: it is the one more terminals read")
	}
}

// TestSequenceSanitizesControlBytes is the injection guard: a goal is text a model
// wrote, and a control byte in it must not be able to end the escape early.
func TestSequenceSanitizesControlBytes(t *testing.T) {
	hostile := "goal\x1b]9;spoofed\x07text\nsecond line\ttab"
	got := Sequence("ti;tle", hostile)

	// Exactly two BEL bytes survive, both terminators, and the hostile text's own
	// escape and BEL are gone.
	if n := strings.Count(got, "\x07"); n != 2 {
		t.Errorf("BEL count = %d, want 2: the payload's own BEL must be stripped, got %q", n, got)
	}
	if n := strings.Count(got, "\x1b"); n != 2 {
		t.Errorf("ESC count = %d, want 2: the payload's own ESC must be stripped, got %q", n, got)
	}
	if strings.Contains(got, "\n") || strings.Contains(got, "\t") {
		t.Errorf("control characters survived: %q", got)
	}
	if !strings.Contains(got, "goal]9;spoofedtext") {
		t.Errorf("the visible text should survive the strip, got %q", got)
	}
	// The title's ";" is the field separator for OSC 777: it must not split the
	// title into a title and a body.
	if !strings.Contains(got, "\x1b]777;notify;ti,tle;") {
		t.Errorf("a title containing ';' must have it replaced, got %q", got)
	}
	// A ";" in the body is harmless (it is the last field) and is kept.
	if !strings.Contains(got, "goal]9;spoofedtext") {
		t.Errorf("the body's text should be kept, got %q", got)
	}
}

// TestThresholdIsACountedBoundary asserts both sides of the gate, because "it
// fired" and "it did not fire" are different claims.
func TestThresholdIsACountedBoundary(t *testing.T) {
	var r recorder
	r.install(t)

	Configure(Config{Channel: ChannelTerminal, MinDuration: 30 * time.Second})

	if Finished(29*time.Second, "t", "b") {
		t.Error("29s must not announce when the minimum is 30s")
	}
	if r.out.Len() != 0 {
		t.Errorf("below the threshold, %d bytes were written, want 0", r.out.Len())
	}
	if len(r.commands) != 0 {
		t.Errorf("below the threshold, %d commands ran, want 0", len(r.commands))
	}

	if !Finished(30*time.Second, "t", "b") {
		t.Error("30s must announce when the minimum is 30s")
	}
	if r.out.Len() == 0 {
		t.Error("at the threshold, nothing was written")
	}
}

func TestThresholdZeroAnnouncesEverything(t *testing.T) {
	var r recorder
	r.install(t)
	Configure(Config{Channel: ChannelTerminal, MinDuration: 0})

	if !Finished(0, "t", "b") {
		t.Error("a zero minimum should announce even an instant completion")
	}
}

// TestChannelMatrix is the whole policy in one table.
func TestChannelMatrix(t *testing.T) {
	cases := []struct {
		channel     Channel
		wantWrite   bool
		wantCommand int
	}{
		{ChannelTerminal, true, 0},
		{ChannelDesktop, false, 1},
		{ChannelBoth, true, 1},
		{ChannelOff, false, 0},
	}
	for _, tc := range cases {
		t.Run(string(tc.channel), func(t *testing.T) {
			var r recorder
			r.install(t)
			Configure(Config{Channel: tc.channel, MinDuration: time.Second})

			emitted := Finished(time.Minute, "t", "b")

			if got := r.out.Len() > 0; got != tc.wantWrite {
				t.Errorf("wrote=%v, want %v (%q)", got, tc.wantWrite, r.out.String())
			}
			if len(r.commands) != tc.wantCommand {
				t.Errorf("commands=%v, want %d", r.commands, tc.wantCommand)
			}
			if tc.channel == ChannelOff && emitted {
				t.Error("ChannelOff reported an emission")
			}
			if tc.channel != ChannelOff && !emitted {
				t.Error("a live channel reported no emission")
			}
		})
	}
}

// TestDesktopPassesTextAsArguments: a goal containing a quote must not become part
// of the script that runs.
func TestDesktopPassesTextAsArguments(t *testing.T) {
	var r recorder
	r.install(t)
	Configure(Config{Channel: ChannelDesktop, MinDuration: 0})

	title := `ti"tle`
	body := `body " with 'quotes' and $(rm -rf /)`
	if !Finished(time.Minute, title, body) {
		t.Fatal("the desktop channel reported no emission")
	}
	if len(r.commands) != 1 {
		t.Fatalf("commands = %v, want exactly one", r.commands)
	}
	argv := r.commands[0]
	if argv[len(argv)-2] != title || argv[len(argv)-1] != body {
		t.Errorf("the last two argv entries should be the raw title and body, got %q", argv)
	}
	if strings.Contains(strings.Join(argv, " "), "display notification "+body) {
		t.Error("the body was interpolated into the script text instead of being passed as an argument")
	}
}

// TestMissingNotifierIsNotAnError keeps a machine without the binary quiet rather
// than noisy: the run it was announcing has already finished.
func TestMissingNotifierIsNotAnError(t *testing.T) {
	var r recorder
	r.install(t)
	r.lookErr = errors.New("not found")
	Configure(Config{Channel: ChannelDesktop, MinDuration: 0})

	if Finished(time.Minute, "t", "b") {
		t.Error("with no notifier installed nothing was emitted, so the result should be false")
	}
	if len(r.commands) != 0 {
		t.Errorf("a missing binary still ran %v", r.commands)
	}
}

// TestNilSinkIsSafe: a caller may have taken the writer away (the tests above do),
// and announcing must not panic.
func TestNilSinkIsSafe(t *testing.T) {
	Reset()
	t.Cleanup(Reset)
	SetSink(nil)
	Configure(Config{Channel: ChannelTerminal, MinDuration: 0})

	if Finished(time.Minute, "t", "b") {
		t.Error("with no sink there is nothing to emit")
	}
}

// TestNormalizeRepairsAnUnknownChannel: a typo should not silently switch the
// feature off.
func TestNormalizeRepairsAnUnknownChannel(t *testing.T) {
	got := Normalize(Config{Channel: "popup", MinDuration: -1})
	if got.Channel != ChannelTerminal {
		t.Errorf("channel = %q, want the default %q", got.Channel, ChannelTerminal)
	}
	if got.MinDuration != DefaultMinDuration {
		t.Errorf("a negative minimum = %v, want the default %v", got.MinDuration, DefaultMinDuration)
	}
	if off := Normalize(Config{Channel: ChannelOff}); off.Channel != ChannelOff {
		t.Errorf("an explicit off became %q", off.Channel)
	}
}

// TestZeroConfigStaysZero: Normalize must not turn "no minimum" into the default
// minimum, or a caller could not ask for everything to be announced.
func TestZeroConfigStaysZero(t *testing.T) {
	if got := Normalize(Config{Channel: ChannelTerminal, MinDuration: 0}); got.MinDuration != 0 {
		t.Errorf("MinDuration = %v, want 0 kept", got.MinDuration)
	}
}
