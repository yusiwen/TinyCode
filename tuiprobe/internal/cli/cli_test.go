package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yusiwen/TinyCode/tuiprobe/internal/scenario"
)

func run(args []string, stdin string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Run(args, strings.NewReader(stdin), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestReplayPrintsTheScreenAsText(t *testing.T) {
	code, out, errOut := run([]string{"replay", "--size", "20x3"}, "hello\r\nworld\r\n")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	if want := "hello\nworld\n\n"; out != want {
		t.Errorf("stdout = %q, want %q", out, want)
	}
}

func TestReplayKeepsStylingInANSIAndHTML(t *testing.T) {
	stream := "\x1b[1;31mred\x1b[0m"

	code, out, _ := run([]string{"replay", "--size", "10x1", "--format", "ansi"}, stream)
	if code != 0 || !strings.Contains(out, "\x1b[0;1;31m") {
		t.Errorf("ansi output lost the style (exit %d): %q", code, out)
	}

	code, out, _ = run([]string{"replay", "--size", "10x1", "--format", "html"}, stream)
	if code != 0 || !strings.Contains(out, "font-weight:700") {
		t.Errorf("html output lost the style (exit %d): %q", code, out)
	}
}

func TestReplayRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"bad size", []string{"replay", "--size", "80"}},
		{"zero width", []string{"replay", "--size", "0x24"}},
		{"bad format", []string{"replay", "--size", "80x24", "--format", "svg"}},
		{"missing file", []string{"replay", "--stream", "/nonexistent/stream.bin"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, _, errOut := run(tc.args, ""); code != 2 || errOut == "" {
				t.Errorf("exit = %d, stderr = %q, want 2 and a message", code, errOut)
			}
		})
	}
}

func TestVersionAndUnknownCommand(t *testing.T) {
	if code, out, _ := run([]string{"version"}, ""); code != 0 || !strings.HasPrefix(out, "tuiprobe ") {
		t.Errorf("version: exit %d, out %q", code, out)
	}
	code, _, errOut := run([]string{"nope"}, "")
	if code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Errorf("unknown command: exit %d, stderr %q", code, errOut)
	}
	if code, _, _ := run(nil, ""); code != 2 {
		t.Errorf("no arguments should be a usage error, got exit %d", code)
	}
}

// TestHelpListsEveryStep is the guard for issue #145: the help enumerated ten of
// the thirteen verbs for two releases, so `screenshot`/`shot` appeared in no surface
// a user reads before the reference document. The enumeration is rendered from
// scenario.Verbs() now, and this compares the *printed* help against that authority —
// editing a literal back in around the generated list fails here.
func TestHelpListsEveryStep(t *testing.T) {
	var out strings.Builder
	usage(&out)
	help := out.String()

	start := strings.Index(help, "steps: ")
	if start < 0 {
		t.Fatal(`the help no longer has a "steps: " enumeration for this check to read`)
	}
	rest := help[start+len("steps: "):]
	end := strings.Index(rest, "\n\n")
	if end < 0 {
		t.Fatal("the steps enumeration is not followed by a blank line")
	}

	listed := map[string]bool{}
	for _, verb := range strings.FieldsFunc(rest[:end], func(r rune) bool {
		return r == ',' || r == ' ' || r == '\n'
	}) {
		listed[verb] = true
	}
	if len(listed) < 10 {
		t.Fatalf("parsed %d steps out of the help, want the whole vocabulary: %q", len(listed), rest[:end])
	}

	authority := map[string]bool{}
	for _, verb := range scenario.Verbs() {
		authority[verb] = true
		if !listed[verb] {
			t.Errorf("scenario.Verbs() lists %q and the help does not: the enumeration is generated "+
				"from that list, so the help was edited around it", verb)
		}
	}
	for verb := range listed {
		if !authority[verb] {
			t.Errorf("the help lists %q, which the runner does not accept", verb)
		}
	}
}
