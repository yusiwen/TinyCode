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
	srcs := []lineSrc{
		{MsgIdx: 0, SourceField: "user", Text: "> Hi"},
	}
	pos := posFromCoord(5, 0, srcs)
	if pos.Offset != -1 {
		t.Errorf("want Offset=-1, got %d", pos.Offset)
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

// --- highlightSelection tests ---

func TestHighlightSingleLinePartial(t *testing.T) {
	lines := []string{"> Hello World"}
	srcs := []lineSrc{{SourceField: "user"}}
	result := highlightSelection(lines, srcs, 0, 2, 0, 6)
	// Selected chars 2-6: "Hello"
	if len(result) != 1 {
		t.Fatalf("expected 1 line, got %d", len(result))
	}
	line := result[0]
	if !strings.Contains(line, "Hello") {
		t.Errorf("expected 'Hello' selected, got %q", line)
	}
	// Should NOT contain "> " (unselected) or " World" (unselected)
	if !strings.Contains(line, "> ") || !strings.Contains(line, " World") {
		t.Errorf("unselected parts missing: %q", line)
	}
}

func TestHighlightSingleLineFull(t *testing.T) {
	lines := []string{"> Hello"}
	srcs := []lineSrc{{SourceField: "user"}}
	result := highlightSelection(lines, srcs, 0, 0, 0, 6)
	// This should wrap the entire line in selectedStyle
	if len(result) != 1 {
		t.Fatalf("expected 1 line, got %d", len(result))
	}
	if !strings.Contains(result[0], "> Hello") {
		t.Errorf("expected content, got %q", result[0])
	}
}

func TestHighlightMultiLine(t *testing.T) {
	lines := []string{"first", "second", "third", "fourth"}
	srcs := []lineSrc{
		{SourceField: "user"},
		{SourceField: "content"},
		{SourceField: "content"},
		{SourceField: "content"},
	}
	// Select from line 1 col 1 to line 2 col 4
	result := highlightSelection(lines, srcs, 1, 1, 2, 4)
	if len(result) != 4 {
		t.Fatalf("expected 4 lines, got %d", len(result))
	}
	// Line 0: unchanged
	if result[0] != "first" {
		t.Errorf("line 0 should be unchanged, got %q", result[0])
	}
	// Line 1: only "irst" (cols 1-end) selected
	if !strings.Contains(result[1], "econd") {
		t.Errorf("line 1 partial selection wrong: %q", result[1])
	}
	// Line 2: "third" fully selected (but with chars 0-4 selected)
	if !strings.Contains(result[2], "third") {
		t.Errorf("line 2 wrong: %q", result[2])
	}
	// Line 3: unchanged
	if result[3] != "fourth" {
		t.Errorf("line 3 should be unchanged, got %q", result[3])
	}
}

func TestHighlightButtonExcluded(t *testing.T) {
	lines := []string{"content", "    [ Copy ]", "next"}
	srcs := []lineSrc{
		{SourceField: "content"},
		{SourceField: "button"},
		{SourceField: "content"},
	}
	result := highlightSelection(lines, srcs, 0, 0, 2, 0)
	// Button line (index 1) should be unchanged
	if result[1] != "    [ Copy ]" {
		t.Errorf("button line should be unchanged, got %q", result[1])
	}
}

func TestHighlightReverseDrag(t *testing.T) {
	// Drag from col 5 to col 0 on same line
	lines := []string{"Hello World"}
	srcs := []lineSrc{{SourceField: "content"}}
	result := highlightSelection(lines, srcs, 0, 5, 0, 0)
	// After normalization: start=0,0 → end=0,5
	if !strings.Contains(result[0], "Hello") {
		t.Errorf("expected reversed selection to work: %q", result[0])
	}
}
