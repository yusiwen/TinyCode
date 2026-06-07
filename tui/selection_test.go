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
		t.Fatal("expected at least label + content + button lines")
	}
	// Should have: content (label+blocks) + button
	hasButton := false
	contentCount := 0
	for _, s := range srcs {
		if s.SourceField == "button" {
			hasButton = true
		}
		if s.SourceField == "content" {
			contentCount++
		}
	}
	if !hasButton {
		t.Error("expected button lineSrc for completed assistant")
	}
	if contentCount < 1 {
		t.Error("expected at least 1 content line")
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
		t.Errorf("expected len(lines)=%d == len(srcs)=%d", len(lines), len(srcs))
	}
}

func TestBuildLineSrcsStreamingNoButton(t *testing.T) {
	msgs := []chatMessage{
		{Role: "assistant", Streaming: true, Content: "partial"},
	}
	_, srcs := buildLineSrcs(msgs, 80)
	for _, s := range srcs {
		if s.SourceField == "button" {
			t.Error("expected no button for streaming message")
		}
	}
}

func TestBuildLineSrcsAssistantWithReasoning(t *testing.T) {
	msgs := []chatMessage{
		{Role: "assistant", ReasoningContent: "thinking...",
			Content: "answer", Blocks: parseMarkdown("answer")},
	}
	_, srcs := buildLineSrcs(msgs, 80)
	// Should have reasoning lines + label + content lines + button
	contentFields := map[string]int{}
	for _, s := range srcs {
		contentFields[s.SourceField]++
	}
	if contentFields["content"] < 2 { // reasoning + label + answer = at least 2 content
		t.Errorf("expected multiple content lines, got: %v", contentFields)
	}
}

func TestBuildLineSrcsMultipleMessages(t *testing.T) {
	count := 5
	msgs := make([]chatMessage, count)
	for i := 0; i < count; i++ {
		msgs[i] = chatMessage{Role: "system", Content: "msg"}
	}
	lines, srcs := buildLineSrcs(msgs, 80)
	if len(lines) != count {
		t.Errorf("expected %d lines, got %d", count, len(lines))
	}
	for i, s := range srcs {
		if s.MsgIdx != i {
			t.Errorf("srcs[%d].MsgIdx = %d, want %d", i, s.MsgIdx, i)
		}
	}
}
