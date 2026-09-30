package tui

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/yusiwen/tinycode/tool"
)

// updateGolden rewrites the golden frames under tui/testdata/golden instead of
// comparing against them: `go test ./tui -run Golden -update`.
var updateGolden = flag.Bool("update", false, "rewrite golden frame files under testdata/golden")

// goldenDir holds the committed frames, relative to the tui package directory.
const goldenDir = "testdata/golden"

// resetStyleCache drops the memoized CellStyle -> lipgloss.Style conversions.
// A cached style carries the renderer state it was built with, so the cache has
// to be emptied whenever the renderer's color profile changes.
func resetStyleCache() {
	styleMu.Lock()
	styleCache = map[CellStyle]lipgloss.Style{}
	styleMu.Unlock()
}

// withTrueColor runs fn with the renderer pinned to the TrueColor profile so
// the frame carries real SGR sequences. Without it lipgloss sees a non-TTY
// stdout, falls back to the Ascii profile, and every style silently vanishes
// from the frame - which is exactly the state the old tests were blind to.
func withTrueColor(t *testing.T, fn func()) {
	t.Helper()
	previous := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	resetStyleCache()
	defer func() {
		lipgloss.SetColorProfile(previous)
		resetStyleCache()
	}()
	fn()
}

// oscSequence matches an OSC escape (ESC ] ... BEL, or ESC ] ... ESC \), which
// is how the banner carries its hyperlink target. stripANSIView only removes
// CSI sequences, so the plain-text form needs this pass on top of it.
var oscSequence = regexp.MustCompile("\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")

// normalizeFrame reduces a frame to its reviewable text form: escape sequences
// stripped, carriage returns removed, trailing blanks trimmed per line and the
// trailing blank lines dropped. Two frames that differ only in fixed-width
// padding therefore compare equal.
func normalizeFrame(frame string) string {
	plain := strings.ReplaceAll(stripANSIView(oscSequence.ReplaceAllString(frame, "")), "\r\n", "\n")
	lines := strings.Split(plain, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n") + "\n"
}

// frameDiff reports the first line that differs, with one line of context and
// the first differing column, so a failure reads like a review comment instead
// of a wall of text.
func frameDiff(want, got string) string {
	wantLines := strings.Split(want, "\n")
	gotLines := strings.Split(got, "\n")
	limit := len(wantLines)
	if len(gotLines) > limit {
		limit = len(gotLines)
	}
	for i := 0; i < limit; i++ {
		var w, g string
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w == g {
			continue
		}
		col := 0
		for col < len(w) && col < len(g) && w[col] == g[col] {
			col++
		}
		var b strings.Builder
		fmt.Fprintf(&b, "first differing line %d (column %d):\n", i+1, col+1)
		if i > 0 {
			fmt.Fprintf(&b, "  context: %q\n", wantLines[i-1])
		}
		fmt.Fprintf(&b, "  want: %q\n", w)
		fmt.Fprintf(&b, "  got:  %q\n", g)
		return b.String()
	}
	return "frames differ only in trailing blank lines\n"
}

// assertGolden compares got against testdata/golden/name, writing the file when
// -update is set. A missing file is a failure, not an implicit write: a test
// run must never silently create the baseline it compares against.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join(goldenDir, name)

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("create golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\nregenerate the frames with: go test ./tui -run Golden -update", path, err)
	}
	if string(want) == got {
		return
	}
	t.Errorf("frame does not match %s\n%s\ngolden: %s", path, frameDiff(string(want), got), path)
}

// --- Scenario builders ---------------------------------------------------

// frameSize is one terminal geometry a scenario is captured at.
type frameSize struct {
	W, H int
}

func (s frameSize) String() string { return fmt.Sprintf("%dx%d", s.W, s.H) }

// frameModel builds a ready, sized model without any dependency on the real
// constructor, the way the other layout tests do. sessionStart is left at the
// zero time on purpose: time.Since(zero) saturates at the maximum duration, so
// the status bar's session clock renders a constant string instead of the
// wall-clock value a golden could never match.
func frameModel(w, h int) *TuiModel {
	m := &TuiModel{
		ready:        true,
		width:        w,
		height:       h,
		status:       StatusIdle,
		modeName:     "plan",
		selectStart:  -1,
		selectEnd:    -1,
		charSelStart: selPos{Offset: -1},
		charSelEnd:   selPos{Offset: -1},
		historyPos:   -1,
	}
	m.vp = viewport.New(w, h-2)
	m.spinner = spinner.New()
	m.spinner.Style = spinnerStyle
	m.input = textarea.New()
	m.input.Placeholder = "Type your request (Ctrl+J for newline)..."
	m.input.SetWidth(w)
	m.input.SetHeight(1)
	return m
}

// frameWelcome renders the startup banner the real model seeds itself with.
func frameWelcome(w, h int) *TuiModel {
	m := frameModel(w, h)
	m.messages = []chatMessage{
		newWelcomeMessage(welcomeInfo{Tools: 24, Skills: 2, Agents: 6}),
	}
	return m
}

// frameMarkdown exercises the rich path: heading, inline code, fenced code,
// bullet list, table and CJK/emoji width handling.
func frameMarkdown(w, h int) *TuiModel {
	m := frameModel(w, h)
	answer := strings.Join([]string{
		"## Retry the HTTP client",
		"",
		"The `doRequest` helper returns the error unchanged, so a single 502",
		"ends the whole run. Wrap it in a bounded retry:",
		"",
		"```go",
		"func (c *Client) do(ctx context.Context) (*Response, error) {",
		"\tfor attempt := 0; attempt < 3; attempt++ {",
		"\t\tresp, err := c.roundTrip(ctx)",
		"\t\tif err == nil {",
		"\t\t\treturn resp, nil",
		"\t\t}",
		"\t}",
		"\treturn nil, ErrRetriesExhausted",
		"}",
		"```",
		"",
		"Notes:",
		"",
		"- Back off between attempts (250ms, 500ms).",
		"- Do not retry a 4xx: the request itself is wrong.",
		"- 中文与 emoji 的宽度也要对齐 🙂",
		"",
		"| attempt | delay |",
		"| ------- | ----- |",
		"| 1       | 250ms |",
		"| 2       | 500ms |",
	}, "\n")
	m.messages = []chatMessage{
		{Role: "user", Content: "add a retry to the http client"},
		{Role: "assistant", Content: answer, Blocks: parseMarkdown(answer)},
	}
	return m
}

// frameStreaming is the mid-answer state: a partial assistant turn and the
// spinner in the status bar.
func frameStreaming(w, h int) *TuiModel {
	m := frameModel(w, h)
	m.status = StatusStreaming
	partial := "Reading `agent/agent.go` and checking the tool loop:\n\n- step budget\n- concurrent tool calls"
	m.messages = []chatMessage{
		{Role: "user", Content: "why does the loop stop after one tool call?"},
		{Role: "assistant", Content: partial, Blocks: parseMarkdown(partial)},
	}
	return m
}

// frameTodo captures the TODO section that every assistant message carries.
func frameTodo(w, h int) *TuiModel {
	m := frameModel(w, h)
	m.messages = []chatMessage{
		{Role: "user", Content: "port the retry logic"},
		{
			Role:    "assistant",
			Content: "Three steps left.",
			ToolCalls: []ToolCallInfo{
				{Name: "bash", Arg: "go test ./agent/..."},
			},
			TodoSnapshot: []tool.TodoItem{
				{ID: "1", Content: "Read the current retry helper", Status: "completed"},
				{ID: "2", Content: "Add bounded retry with backoff", Status: "in_progress"},
				{ID: "3", Content: "Cover the 4xx path with a test", Status: "pending"},
				{ID: "4", Content: "Update CHANGELOG.md", Status: "pending"},
			},
		},
	}
	m.todoDirty = true
	return m
}

// frameDialog is the permission prompt the sandbox raises for a blocked path.
func frameDialog(w, h int) *TuiModel {
	m := frameModel(w, h)
	m.messages = []chatMessage{
		{Role: "user", Content: "clean the build directory"},
		{
			Role:      "assistant",
			Content:   "I need to remove files outside the sandbox root.",
			ToolCalls: []ToolCallInfo{{Name: "bash", Arg: "rm -rf ../build"}},
		},
	}
	m.dialogMode = true
	m.dialogMsg = "Allow write outside the project root? ../build"
	m.dialogItems = []string{"Allow once", "Allow for this session", "Deny"}
	m.dialogSel = 0
	return m
}

// framePalette is the floating command palette with the selection on the first
// entry.
func framePalette(w, h int) *TuiModel {
	m := frameModel(w, h)
	m.messages = []chatMessage{
		{Role: "user", Content: "switch to build mode"},
		{Role: "assistant", Content: "Use /build or press Tab.", Blocks: parseMarkdown("Use `/build` or press **Tab**.")},
	}
	m.cmdPalette = true
	m.cmdPaletteInput = ""
	m.cmdPaletteSel = 0
	return m
}

// frameDiagnostics shows the status bar carrying LSP errors next to a normal
// turn.
func frameDiagnostics(w, h int) *TuiModel {
	m := frameModel(w, h)
	m.messages = []chatMessage{
		{Role: "user", Content: "fix the diagnostics"},
		{Role: "assistant", Content: "gopls reports two unused imports.", Blocks: parseMarkdown("gopls reports **two** unused imports.")},
	}
	m.diagTotal = 3
	m.diagFiles = 2
	m.diagFile = "agent/agent.go"
	m.diagDetails = []string{"agent/agent.go: 2", "tool/edit.go: 1"}
	m.statusMsg = "  diagnostics refreshed"
	return m
}

// frameLongOutput is the overflow case: more answer text than the viewport can
// hold, so the frame must show the tail without corrupting the layout.
func frameLongOutput(w, h int) *TuiModel {
	m := frameModel(w, h)
	lines := make([]string, 0, 80)
	lines = append(lines, "The tool loop, step by step:", "")
	for i := 1; i <= 60; i++ {
		lines = append(lines, fmt.Sprintf("%2d. step %d executed and its result appended to the history", i, i))
	}
	answer := strings.Join(lines, "\n")
	m.messages = []chatMessage{
		{Role: "user", Content: "walk me through the loop"},
		{Role: "assistant", Content: answer, Blocks: parseMarkdown(answer)},
	}
	return m
}

// frameScenario pairs a builder with the geometries it is captured at. Adding a
// tier is a one-line change: append a frameSize and regenerate with
// `go test ./tui -run Golden -update`. Not every scenario needs every terminal:
// 200x50 is reserved for the two that exercise wrapping and overflow, 100x30 for
// a wide-but-not-extreme layout, and 40x12 for the narrow end where the banner
// art is dropped and tables must still line up.
var frameScenarios = []struct {
	name  string
	sizes []frameSize
	build func(w, h int) *TuiModel
}{
	{"welcome", []frameSize{{80, 24}, {120, 40}, {100, 30}}, frameWelcome},
	{"markdown", []frameSize{{80, 24}, {120, 40}, {200, 50}, {100, 30}, {40, 12}}, frameMarkdown},
	{"streaming", []frameSize{{80, 24}, {120, 40}}, frameStreaming},
	{"todo", []frameSize{{80, 24}, {120, 40}, {40, 12}}, frameTodo},
	{"dialog", []frameSize{{80, 24}, {120, 40}, {40, 12}}, frameDialog},
	{"palette", []frameSize{{80, 24}, {120, 40}, {40, 12}}, framePalette},
	{"diagnostics", []frameSize{{80, 24}, {120, 40}}, frameDiagnostics},
	{"longoutput", []frameSize{{80, 24}, {120, 40}, {200, 50}, {100, 30}}, frameLongOutput},
}

// TestGoldenFrames pins the plain-text form of every scenario at every
// geometry. This is the regression net for layout: wrapping, indentation,
// column alignment, overflow and the status bar all show up as a text diff.
func TestGoldenFrames(t *testing.T) {
	for _, sc := range frameScenarios {
		for _, size := range sc.sizes {
			name := fmt.Sprintf("%s_%s", sc.name, size)
			t.Run(name, func(t *testing.T) {
				var plain, ansi string
				withTrueColor(t, func() {
					m := sc.build(size.W, size.H)
					ansi = m.View()
					plain = normalizeFrame(ansi)
				})

				// A frame without a single escape sequence means the colour
				// profile was lost, not that the frame is "plain": guard the
				// mechanism so a silent regression cannot pass.
				if !strings.Contains(ansi, "\x1b[") {
					t.Fatalf("frame %s carries no SGR sequences: the color profile was not pinned", name)
				}
				assertGolden(t, filepath.Join("frames", name+".txt"), plain)
			})
		}
	}
}

// TestGoldenFrameANSI keeps one full ANSI frame under version control, so the
// exact escape sequences (colour, bold, underline, OSC 8 links) are diffable
// and not only their stripped text.
func TestGoldenFrameANSI(t *testing.T) {
	var ansi string
	withTrueColor(t, func() {
		m := frameMarkdown(80, 24)
		ansi = m.View()
	})
	assertGolden(t, filepath.Join("ansi", "markdown_80x24.ansi"), ansi)
}

// TestFrameScenariosRenderTwice proves the incremental render path is stable:
// a second View() on the same model must return the identical frame. A diff
// here means the grid's dirty tracking leaks state between frames.
func TestFrameScenariosRenderTwice(t *testing.T) {
	for _, sc := range frameScenarios {
		for _, size := range sc.sizes {
			name := fmt.Sprintf("%s_%s", sc.name, size)
			t.Run(name, func(t *testing.T) {
				withTrueColor(t, func() {
					m := sc.build(size.W, size.H)
					first := m.View()
					second := m.View()
					if first != second {
						t.Errorf("second render differs from the first\n%s", frameDiff(first, second))
					}
				})
			})
		}
	}
}
