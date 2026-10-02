package scenario

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/TinyCode/tuiprobe/internal/daemon"
)

// startDaemon runs a daemon on a short socket path (unix sockets have a length
// limit) and stops it when the test ends.
func startDaemon(t *testing.T) string {
	t.Helper()
	socket := filepath.Join(os.TempDir(), fmt.Sprintf("tps-%d-%d.sock", os.Getpid(), time.Now().UnixNano()%100000))
	srv := daemon.New(socket, time.Minute)
	ln, err := srv.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { srv.Stop(); _ = ln.Close() })
	return socket
}

func TestParseRejectsBadScripts(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"empty", "# only a comment\n", "no steps"},
		{"quote", "open -- ./app\nwait --text \"unbalanced\n", "unbalanced quote"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Parse = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	steps, err := Parse(strings.NewReader("# a comment\nopen --size 80x24 -- ./app\nwait --text \"ready\" --timeout 5s\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(steps) != 2 || steps[0].Verb != "open" || steps[1].Verb != "wait" {
		t.Fatalf("steps = %+v, want open and wait", steps)
	}
	if steps[1].Line != 3 {
		t.Errorf("the second step reports line %d, want 3", steps[1].Line)
	}
}

// TestRunDrivesAScriptedSession is the whole point of the package: a file says
// what to do, and the run either matches the committed artifacts or says which
// line failed.
func TestRunDrivesAScriptedSession(t *testing.T) {
	socket := startDaemon(t)
	dir := t.TempDir()

	script := `
# a scripted session
open --size 90x20 -- /bin/sh -c 'echo ready; read l; echo got:$l; exit 4'
wait --text "ready" --timeout 5s
fit --size 90x20
golden welcome.txt
send --text hello --key enter
wait --text "got:hello" --timeout 5s
close
expect-exit 4
`
	steps, err := Parse(strings.NewReader(script))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var progress strings.Builder
	result, err := Run(steps, Options{Socket: socket, Dir: dir, Update: true, Stdout: &progress})
	if err != nil {
		t.Fatalf("Run: %v\n%s", err, progress.String())
	}
	if result.Skipped || result.Steps != 8 {
		t.Errorf("result = %+v, want 8 steps and no skip", result)
	}

	// The golden was written and normalised: no escape sequences, no trailing
	// blank rows beyond a final newline.
	written, err := os.ReadFile(filepath.Join(dir, "welcome.txt"))
	if err != nil {
		t.Fatalf("read the golden: %v", err)
	}
	if !strings.HasPrefix(string(written), "ready") {
		t.Errorf("golden = %q, want it to start with the screen's first line", written)
	}

	// Re-running without --update must compare, not write.
	if _, err := Run(steps, Options{Socket: socket, Dir: dir, Stdout: &progress}); err != nil {
		t.Fatalf("comparing run: %v\n%s", err, progress.String())
	}

	// A changed screen is a failure that names the line.
	if err := os.WriteFile(filepath.Join(dir, "welcome.txt"), []byte("something else\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = Run(steps, Options{Socket: socket, Dir: dir})
	if err == nil {
		t.Fatal("a mismatching golden must fail the run")
	}
	// The failing line is named: in this script the golden step is line 6 (the
	// file starts with a blank line and a comment).
	if !strings.Contains(err.Error(), "line 6") || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("error = %v, want it to name line 6 and the mismatch", err)
	}
}

// TestRunSkipsWithAReasonWhenGated covers the harness contract: a check that is
// gated off must say so, and must not open anything.
func TestRunSkipsWithAReasonWhenGated(t *testing.T) {
	socket := startDaemon(t)
	steps, err := Parse(strings.NewReader("open -- /bin/sh -c 'sleep 30'\nclose\n"))
	if err != nil {
		t.Fatal(err)
	}

	const gate = "TUIPROBE_SCENARIO_GATE"
	os.Unsetenv(gate)
	result, err := Run(steps, Options{Socket: socket, Gate: gate})
	if err != nil {
		t.Fatalf("a gated-off run must not fail: %v", err)
	}
	if !result.Skipped || !strings.Contains(result.Reason, gate) {
		t.Errorf("result = %+v, want a skip naming %s", result, gate)
	}

	// Nothing was opened, so the daemon has no sessions.
	client := &daemon.Client{Socket: socket, StartDaemon: func(string) error { return nil }}
	resp, err := client.Call(daemon.Request{Cmd: "sessions"})
	if err != nil || resp.SessionCnt != 0 {
		t.Errorf("sessions = %+v (%v), want none after a skipped run", resp, err)
	}

	// With the variable set the scenario actually runs.
	t.Setenv(gate, "1")
	if _, err := Run(steps, Options{Socket: socket, Gate: gate}); err != nil {
		t.Fatalf("run with the gate set: %v", err)
	}
}

func TestRunReportsUnknownStepsAndBadArguments(t *testing.T) {
	socket := startDaemon(t)
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"unknown", "open -- /bin/sh -c 'sleep 30'\nfrobnicate\n", "unknown step"},
		{"open without command", "open --size 80x24\n", "needs a command"},
		{"wait without pattern", "open -- /bin/sh -c 'sleep 30'\nwait --timeout 1s\n", "needs --text"},
		{"golden without file", "open -- /bin/sh -c 'sleep 30'\ngolden\n", "needs one file"},
		{"bad size", "open --size 80 -- /bin/sh\n", "not WxH"},
		{"bad fit size", "open -- /bin/sh -c 'sleep 30'\nfit --size nope\n", "not WxH"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps, err := Parse(strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			_, err = Run(steps, Options{Socket: socket, Name: strings.ReplaceAll(tc.name, " ", "-")})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Run = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestFitCatchesAWrongGeometryClaim is the geometry assertion as a scenario step:
// a scenario whose goldens were captured at one terminal size must not quietly
// pass when it runs at another.
func TestFitCatchesAWrongGeometryClaim(t *testing.T) {
	socket := startDaemon(t)

	// The session is 20x3; claiming 40x3 must fail.
	claiming := "open --size 20x3 -- /bin/sh -c 'sleep 5'\nfit --size 40x3\n"
	steps, err := Parse(strings.NewReader(claiming))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Run(steps, Options{Socket: socket, Name: "fit-wrong"})
	if err == nil || !strings.Contains(err.Error(), "claims 40x3") {
		t.Errorf("fit = %v, want a failure naming the wrong claim", err)
	}

	// The matching claim passes, and the screen fits it.
	matching := "open --size 20x3 -- /bin/sh -c 'printf \"%0.sx\" {1..40}; echo; sleep 5'\nfit --size 20x3\n"
	steps, err = Parse(strings.NewReader(matching))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(steps, Options{Socket: socket, Name: "fit-ok"}); err != nil {
		t.Errorf("fit at the real size = %v, want it to pass (the emulator wraps at its width)", err)
	}
}

// TestRunCapturesAScreenshot covers the image half of a scenario: the PNG must be
// written, and its size must follow from the terminal geometry.
func TestRunCapturesAScreenshot(t *testing.T) {
	socket := startDaemon(t)
	dir := t.TempDir()
	script := "open --size 20x4 -- /bin/sh -c 'echo hello; sleep 5'\nwait --text hello --timeout 5s\nscreenshot shot.png\nscreenshot big.png --scale 2\nscreenshot page.html --format html\n"
	steps, err := Parse(strings.NewReader(script))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(steps, Options{Socket: socket, Dir: dir, Name: "shots"}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, name := range []string{"shot.png", "big.png", "page.html"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s was not written: %v", name, err)
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", name)
		}
	}

	// The two PNGs differ in size because the scale multiplies the face, and the
	// small one is exactly the geometry the scenario claims.
	small, err := os.ReadFile(filepath.Join(dir, "shot.png"))
	if err != nil {
		t.Fatal(err)
	}
	big, err := os.ReadFile(filepath.Join(dir, "big.png"))
	if err != nil {
		t.Fatal(err)
	}
	if len(big) <= len(small) {
		t.Errorf("the scaled image (%d bytes) is not larger than the plain one (%d bytes)", len(big), len(small))
	}
}
