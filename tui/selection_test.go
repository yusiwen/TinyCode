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
	if pos.Offset != 3 {
		t.Errorf("want offset=3 (last char), got %d", pos.Offset)
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
