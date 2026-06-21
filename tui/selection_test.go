package tui

import (
	"strings"
	"testing"
)

// --- buildLineSrcs tests ---

func TestBuildLineSrcsUser(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "Hi"},
	}
	_, srcs := buildLineSrcs(msgs, 80)
	if len(srcs) == 0 {
		t.Fatal("expected at least 1 lineSrc")
	}
	if srcs[0].SourceField != "user" {
		t.Errorf("expected 'user', got %q", srcs[0].SourceField)
	}
	if !strings.Contains(srcs[0].Text, "Hi") {
		t.Errorf("expected 'Hi' in Text, got %q", srcs[0].Text)
	}
}

func TestBuildLineSrcsSystem(t *testing.T) {
	msgs := []chatMessage{
		{Role: "system", Content: "Switched to build mode"},
	}
	_, srcs := buildLineSrcs(msgs, 80)
	if len(srcs) == 0 {
		t.Fatal("expected at least 1 lineSrc")
	}
	if srcs[0].SourceField != "system" {
		t.Errorf("expected 'system', got %q", srcs[0].SourceField)
	}
}

func TestBuildLineSrcsAssistant(t *testing.T) {
	msgs := []chatMessage{
		{Role: "assistant", Content: "**bold**", Blocks: parseMarkdown("**bold**")},
	}
	_, srcs := buildLineSrcs(msgs, 80)
	if len(srcs) < 2 {
		t.Fatal("expected at least 2 lines (label+content)")
	}
	hasButton := false
	for _, s := range srcs {
		if s.SourceField == "button" {
			hasButton = true
		}
	}
	if !hasButton {
		t.Error("expected button lineSrc for completed assistant")
	}
}

func TestBuildLineSrcsCount(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "**answer**", Blocks: parseMarkdown("**answer**")},
		{Role: "system", Content: "Done"},
	}
	lines, srcs := buildLineSrcs(msgs, 80)
	if len(lines) != len(srcs) {
		t.Errorf("len(lines)=%d != len(srcs)=%d", len(lines), len(srcs))
	}
}

func TestBuildLineSrcsStreamingNoButton(t *testing.T) {
	msgs := []chatMessage{
		{Role: "assistant", Streaming: true, Content: "partial"},
	}
	_, srcs := buildLineSrcs(msgs, 80)
	for _, s := range srcs {
		if s.SourceField == "button" {
			t.Error("no button for streaming message")
		}
	}
}

func TestBuildLineSrcsWithReasoning(t *testing.T) {
	msgs := []chatMessage{
		{Role: "assistant", ReasoningContent: "think...",
			Content: "answer", Blocks: parseMarkdown("answer")},
	}
	_, srcs := buildLineSrcs(msgs, 80)
	if len(srcs) < 2 {
		t.Errorf("expected multiple lines, got %d", len(srcs))
	}
}

func TestBuildLineSrcsMultiMessage(t *testing.T) {
	msgs := make([]chatMessage, 5)
	for i := 0; i < 5; i++ {
		msgs[i] = chatMessage{Role: "system", Content: "m"}
	}
	_, srcs := buildLineSrcs(msgs, 80)
	for i, s := range srcs {
		if s.MsgIdx != i {
			t.Errorf("srcs[%d].MsgIdx=%d, want %d", i, s.MsgIdx, i)
		}
	}
}

// --- posFromCoord tests ---

func TestPosFromCoordFirstChar(t *testing.T) {
	srcs := []lineSrc{
		{MsgIdx: 0, SourceField: "user", Text: "> Hello"},
	}
	pos := posFromCoord(0, 0, srcs)
	if pos.Offset != 0 || pos.MsgIdx != 0 {
		t.Errorf("want MsgIdx=0 Offset=0, got MsgIdx=%d Offset=%d", pos.MsgIdx, pos.Offset)
	}
}

func TestPosFromCoordMiddle(t *testing.T) {
	srcs := []lineSrc{
		{MsgIdx: 0, SourceField: "system", Text: "→ msg"},
	}
	pos := posFromCoord(0, 2, srcs)
	if pos.Offset != 2 {
		t.Errorf("want offset=2, got %d", pos.Offset)
	}
}

func TestPosFromCoordPastEnd(t *testing.T) {
	srcs := []lineSrc{
		{MsgIdx: 0, SourceField: "user", Text: "> Hi"},
	}
	pos := posFromCoord(0, 100, srcs)
	if pos.Offset != 100 {
		t.Errorf("want offset=100 (no clamping, col - 0), got %d", pos.Offset)
	}
}

func TestPosFromCoordButton(t *testing.T) {
	srcs := []lineSrc{
		{MsgIdx: 0, SourceField: "button", Text: ""},
	}
	pos := posFromCoord(0, 0, srcs)
	if pos.Offset != -1 {
		t.Errorf("want Offset=-1, got %d", pos.Offset)
	}
}

func TestPosFromCoordOutOfRange(t *testing.T) {
	// Click beyond srcs should clamp to last valid row
	srcs := []lineSrc{
		{MsgIdx: 0, SourceField: "user", Text: "> Hi"},
	}
	pos := posFromCoord(5, 2, srcs)
	if pos.Offset < 0 {
		t.Errorf("want clamped offset>=0, got %d", pos.Offset)
	}
	if pos.Offset != 2 {
		t.Errorf("want Offset=2 (clamped to row 0, col 2 - ContentOffset 0), got %d", pos.Offset)
	}
}

func TestPosFromCoordClampLastLine(t *testing.T) {
	// Drag past the last content line should clamp to the final valid row
	srcs := []lineSrc{
		{MsgIdx: 0, SourceField: "user", Text: "Line 1"},
		{MsgIdx: 0, SourceField: "user", Text: "Line 2"},
		{MsgIdx: 0, SourceField: "user", Text: "Line 3"},
	}
	pos := posFromCoord(5, 0, srcs)
	if pos.Offset < 0 {
		t.Errorf("want clamped offset>=0, got %d", pos.Offset)
	}
	if pos.MsgIdx != 0 {
		t.Errorf("want MsgIdx=0, got %d", pos.MsgIdx)
	}
}

func TestPosFromCoordNegativeLine(t *testing.T) {
	srcs := []lineSrc{
		{MsgIdx: 0, SourceField: "user", Text: "> Hi"},
	}
	pos := posFromCoord(-1, 0, srcs)
	if pos.Offset != -1 {
		t.Errorf("want Offset=-1 for negative line, got %d", pos.Offset)
	}
}

func TestPosFromCoordLastLine(t *testing.T) {
	// Last row of a multi-row message should be selectable
	srcs := []lineSrc{
		{MsgIdx: 0, SourceField: "user", Text: "> Line 1", ContentOffset: 2},
		{MsgIdx: 0, SourceField: "user", Text: "> Line 2", ContentOffset: 2},
	}
	pos := posFromCoord(1, 4, srcs)
	if pos.Offset != 2 {
		t.Errorf("want Offset=2 (col 4 - offset 2), got %d", pos.Offset)
	}
	if pos.MsgIdx != 0 {
		t.Errorf("want MsgIdx=0, got %d", pos.MsgIdx)
	}
}

func TestPosFromCoordDragToEnd(t *testing.T) {
	// Simulate drag from first row to last row — both should be valid
	srcs := []lineSrc{
		{MsgIdx: 0, SourceField: "assistant", Text: "    First line", ContentOffset: 4},
		{MsgIdx: 0, SourceField: "assistant", Text: "    Second line", ContentOffset: 4},
		{MsgIdx: 0, SourceField: "assistant", Text: "    Third line", ContentOffset: 4},
	}
	// Start selection on row 0
	start := posFromCoord(0, 4, srcs)
	if start.Offset != 0 {
		t.Errorf("start: want Offset=0, got %d", start.Offset)
	}
	// Drag to row 2 (last row)
	end := posFromCoord(2, 10, srcs)
	if end.Offset < 0 {
		t.Errorf("drag endpoint: Offset=-1 (invalid), want valid position")
	}
	if end.Offset != 6 {
		t.Errorf("end: want Offset=6 (col 10 - offset 4), got %d", end.Offset)
	}
}

func TestPosFromCoordNegativeCol(t *testing.T) {
	srcs := []lineSrc{
		{MsgIdx: 0, SourceField: "user", Text: "> Hi"},
	}
	pos := posFromCoord(0, -5, srcs)
	if pos.Offset != 0 {
		t.Errorf("want offset=0, got %d", pos.Offset)
	}
}

// --- extractSelected tests ---

func TestExtractSelectedSingleMsg(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "Hello World"},
	}
	text := extractSelected(selPos{MsgIdx: 0, Offset: 0}, selPos{MsgIdx: 0, Offset: 4}, msgs)
	if text != "Hello" {
		t.Errorf("want 'Hello', got %q", text)
	}
}

func TestExtractSelectedFullMsg(t *testing.T) {
	msgs := []chatMessage{
		{Role: "system", Content: "mode changed"},
	}
	text := extractSelected(selPos{MsgIdx: 0, Offset: 0}, selPos{MsgIdx: 0, Offset: 12}, msgs)
	if text != "mode changed" {
		t.Errorf("want 'mode changed', got %q", text)
	}
}

func TestExtractSelectedCrossMsg(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "World"},
	}
	text := extractSelected(selPos{MsgIdx: 0, Offset: 1}, selPos{MsgIdx: 1, Offset: 2}, msgs)
	if text != "ello\nWor" {
		t.Errorf("want 'ello\\nWor', got %q", text)
	}
}

func TestExtractSelectedReverse(t *testing.T) {
	msgs := []chatMessage{
		{Role: "user", Content: "ABCDEF"},
	}
	text := extractSelected(selPos{MsgIdx: 0, Offset: 5}, selPos{MsgIdx: 0, Offset: 1}, msgs)
	if text != "BCDEF" {
		t.Errorf("want 'BCDEF' (reversed, inclusive), got %q", text)
	}
}

func TestExtractSelectedEmpty(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "test"}}
	text := extractSelected(selPos{Offset: -1}, selPos{MsgIdx: 0, Offset: 2}, msgs)
	if text != "" {
		t.Errorf("want empty for invalid start, got %q", text)
	}
}

func TestExtractSelectedOutOfRange(t *testing.T) {
	msgs := []chatMessage{{Role: "user", Content: "hi"}}
	text := extractSelected(selPos{MsgIdx: 5, Offset: 0}, selPos{MsgIdx: 5, Offset: 1}, msgs)
	if text != "" {
		t.Errorf("want empty for out-of-range, got %q", text)
	}
}
