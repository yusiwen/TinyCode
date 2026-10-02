package tui

// The project's half of TUI verification: the scenarios, and the content they show.
//
// Everything generic — the terminal emulator, golden files, the PNG renderer, the PTY
// session, the browser renderer — lives in the tuiprobe module (pinned in go.mod), so
// this file is fixtures and judgments only: which screens matter, at which geometries,
// built from the project's own model. See docs/tui-verification.md.

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"

	"github.com/yusiwen/TinyCode/tuiprobe/golden"
	"github.com/yusiwen/tinycode/tool"
)

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

// frameCompressing is /compress in flight (issue #3): the summarizer runs off
// the event loop, so the transcript stays interactive and the only sign of work
// is the cancellable status message in the status bar.
func frameCompressing(w, h int) *TuiModel {
	m := frameModel(w, h)
	m.messages = []chatMessage{
		{Role: "user", Content: "keep going with the refactor"},
		{Role: "assistant", Content: "History is close to the context limit.", Blocks: parseMarkdown("History is close to the context limit.")},
	}
	// The state beginCompress() holds while the summarizer runs; a run cannot
	// start until it clears.
	m.compressActive = true
	m.ShowStatus("Compressing history… (Ctrl+C to cancel)")
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

// frameScenarios is the single table the suite drives: goldens, the ANSI golden,
// determinism, geometry, PNGs and the stream screenshot all read it.
//
// golden.Sizes lists the geometries whose *text* is committed (27 files today);
// shots lists the images worth looking at, which is a smaller set at geometries the
// goldens may not cover (the narrow end of a table, a 112-column status line).
// Adding a screen is one entry here plus an intended layout change.
var frameScenarios = []struct {
	name  string
	sizes []golden.Size
	shots []shotSpec
	build func(w, h int) *TuiModel
}{
	{"welcome", []golden.Size{{W: 80, H: 24}, {W: 120, H: 40}, {W: 100, H: 30}},
		[]shotSpec{{"welcome", golden.Size{W: 80, H: 24}}}, frameWelcome},
	{"markdown", []golden.Size{{W: 80, H: 24}, {W: 120, H: 40}, {W: 200, H: 50}, {W: 100, H: 30}, {W: 40, H: 12}},
		[]shotSpec{
			{"markdown", golden.Size{W: 80, H: 24}},
			// The narrow end is worth an image as well as a golden: at 40 columns the
			// banner art is dropped and every table has to be re-laid out.
			{"narrow", golden.Size{W: 40, H: 12}},
		}, frameMarkdown},
	{"streaming", []golden.Size{{W: 80, H: 24}, {W: 120, H: 40}}, nil, frameStreaming},
	{"todo", []golden.Size{{W: 80, H: 24}, {W: 120, H: 40}, {W: 40, H: 12}},
		[]shotSpec{{"todo", golden.Size{W: 80, H: 24}}}, frameTodo},
	{"dialog", []golden.Size{{W: 80, H: 24}, {W: 120, H: 40}, {W: 40, H: 12}},
		[]shotSpec{{"dialog", golden.Size{W: 80, H: 24}}}, frameDialog},
	{"palette", []golden.Size{{W: 80, H: 24}, {W: 120, H: 40}, {W: 40, H: 12}}, nil, framePalette},
	{"diagnostics", []golden.Size{{W: 80, H: 24}, {W: 120, H: 40}}, nil, frameDiagnostics},
	{"compressing", []golden.Size{{W: 80, H: 24}, {W: 40, H: 12}},
		[]shotSpec{
			// The golden pins the compressing status line's text; the images show its
			// styling and where the 112-column line is truncated, at 80 and at 40
			// columns (issue #25).
			{"compressing", golden.Size{W: 80, H: 24}},
			{"compressing-narrow", golden.Size{W: 40, H: 12}},
		}, frameCompressing},
	{"longoutput", []golden.Size{{W: 80, H: 24}, {W: 120, H: 40}, {W: 200, H: 50}, {W: 100, H: 30}},
		[]shotSpec{{"longoutput", golden.Size{W: 120, H: 40}}}, frameLongOutput},
}

// shotSpec names one image: the scenario name it is filed under and the geometry.
type shotSpec struct {
	name string
	size golden.Size
}
