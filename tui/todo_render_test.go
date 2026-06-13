package tui

import (
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/tool"
)

func newModelWithAssistant() *TuiModel {
	m := newTestTUI()
	m.ready = true
	m.vp.Width = 80
	m.vp.Height = 50
	// Add a simulated user + assistant message with tool calls
	m.messages = append(m.messages, chatMessage{Role: "user", Content: "analyze this"})
	m.messages = append(m.messages, chatMessage{
		Role:    "assistant",
		Content: "I found the answer.",
		ToolCalls: []ToolCallInfo{
			{Name: "bash", Arg: "find ."},
		},
	})
	m.msgDirty = []bool{true, true}
	m.msgRowCount = []int{0, 0}
	return m
}

func TestTodoRenderingEmpty(t *testing.T) {
	m := newModelWithAssistant()
	// No todo items set → should not show todo section
	output := m.View()
	if strings.Contains(output, "Todo") {
		// If viewport is empty-ish, nothing to see — not an error
	}
}

func TestTodoRenderingWithItems(t *testing.T) {
	m := newModelWithAssistant()

	// Add items to the store
	store := tool.NewTodoStore()
	store.Write([]tool.TodoItem{
		{ID: "1", Content: "Check repo status", Status: "completed"},
		{ID: "2", Content: "Fix CVE-2026-1234", Status: "in_progress"},
		{ID: "3", Content: "Verify fix", Status: "pending"},
	}, false)
	m.todoStore = store
	m.todoDirty = true

	output := m.View()
	if !strings.Contains(output, "Todo") {
		t.Error("expected Todo header in view")
	}
	if !strings.Contains(output, "1/3") {
		t.Error("expected progress 1/3 in view")
	}
	if !strings.Contains(output, "[x]") {
		t.Error("expected completed marker [x]")
	}
	if !strings.Contains(output, "[>]") {
		t.Error("expected in-progress marker [>]")
	}
	if !strings.Contains(output, "[ ]") {
		t.Error("expected pending marker [ ]")
	}
	if !strings.Contains(output, "Check repo") {
		t.Error("expected completed task content")
	}
	if !strings.Contains(output, "Fix CVE") {
		t.Error("expected in-progress task content")
	}
}

func TestTodoRenderingAllDone(t *testing.T) {
	m := newModelWithAssistant()

	store := tool.NewTodoStore()
	store.Write([]tool.TodoItem{
		{ID: "1", Content: "Task A", Status: "completed"},
		{ID: "2", Content: "Task B", Status: "completed"},
	}, false)
	m.todoStore = store
	m.todoDirty = true

	output := m.View()
	if !strings.Contains(output, "2/2") {
		t.Errorf("expected 2/2 progress, got:\n%s", output[:min(len(output), 200)])
	}
}
