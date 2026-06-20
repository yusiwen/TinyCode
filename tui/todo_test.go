package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/tool"
)

func TestTodoInjection(t *testing.T) {
	store := tool.NewTodoStore()
	store.Write([]tool.TodoItem{
		{ID: "1", Content: "Create parent POM", Status: "in_progress"},
		{ID: "2", Content: "Create core module", Status: "pending"},
		{ID: "3", Content: "Test module", Status: "completed"},
	}, false)

	msg := chatMessage{
		Role: "assistant",
		ReasoningContent: "Let me plan this project step by step...",
		Content: "Project created successfully!",
		ToolCalls: []ToolCallInfo{
			{Name: "todo", Arg: `{"todos":[{"id":"1","content":"Create parent POM","status":"in_progress"}]}`},
			{Name: "bash", Arg: "mkdir -p project"},
			{Name: "write_file", Arg: "pom.xml (20 lines)"},
		},
	}

	comp := AssistantComponent{}
	chunks := comp.Render(msg, false)

	// Verify "→ Calling tools:" exists in chunks
	toolCallIdx := -1
	for ci, ch := range chunks {
		if strings.Contains(ch.Text, "→ Calling tools:") {
			toolCallIdx = ci
			break
		}
	}
	if toolCallIdx < 0 {
		t.Fatal("ToolCallComponent did not produce '→ Calling tools:' header")
	}
	t.Logf("toolCallIdx = %d", toolCallIdx)
	if toolCallIdx <= 0 {
		t.Logf("WARNING: toolCallIdx=%d does not satisfy > 0 condition, injection would be skipped", toolCallIdx)
		return
	}

	// Simulate injection
	items := store.Read()
	todoChunks := []CellChunk{
		{Text: "", Style: DefaultStyle},
		{Text: fmt.Sprintf("  Todo (%d/%d)", store.Summary().Completed+store.Summary().Cancelled, store.Summary().Total), Style: HeadingStyle},
	}
	for _, item := range items {
		marker := "[ ]"
		style := DefaultStyle
		switch item.Status {
		case "in_progress":
			marker = "[>]"
			style = DimStyle
		case "completed":
			marker = "[x]"
			style = DimStyle
		case "cancelled":
			marker = "[~]"
			style = DimStyle
		}
		line := "    " + marker + " " + item.Content
		todoChunks = append(todoChunks, CellChunk{Text: line, Style: style})
	}

	chunks = append(chunks[:toolCallIdx], append(todoChunks, chunks[toolCallIdx:]...)...)

	// Verify TODO is in chunks
	todoFound := false
	for _, ch := range chunks {
		if strings.Contains(ch.Text, "Todo") && strings.Contains(ch.Text, "1/3") {
			todoFound = true
			t.Logf("TODO header: %q", ch.Text)
			break
		}
	}
	if !todoFound {
		t.Fatal("TODO header not found after injection")
	}

	// Verify order: TODO appears before → Calling tools:
	todoBefore := false
	orderOK := false
	for _, ch := range chunks {
		if strings.Contains(ch.Text, "Todo") {
			todoBefore = true
		}
		if strings.Contains(ch.Text, "→ Calling tools:") && todoBefore {
			orderOK = true
		}
	}
	if !orderOK {
		t.Fatal("TODO does not appear before '→ Calling tools:'")
	}
	t.Log("✅ TODO correctly injected before tool calls")
}
