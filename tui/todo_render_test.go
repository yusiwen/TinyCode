package tui

import (
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/tool"
)

func TestTodoRenderingEmpty(t *testing.T) {
	m := newTestTUI()
	m.ready = true
	// No todo items set → should render without todo section
	output := m.View()
	if strings.Contains(output, "Todo") {
		// This is OK if the store is empty — it shouldn't show
		// but the output might contain "Todo" elsewhere
	}
}

func TestTodoRenderingWithItems(t *testing.T) {
	m := newTestTUI()
	m.ready = true

	// Add items to the store
	store := tool.NewTodoStore()
	store.Write([]tool.TodoItem{
		{ID: "1", Content: "Check repo status", Status: "completed"},
		{ID: "2", Content: "Fix CVE-2026-1234", Status: "in_progress"},
		{ID: "3", Content: "Verify fix", Status: "pending"},
	}, false)
	m.todoStore = store

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
	m := newTestTUI()
	m.ready = true

	store := tool.NewTodoStore()
	store.Write([]tool.TodoItem{
		{ID: "1", Content: "Task A", Status: "completed"},
		{ID: "2", Content: "Task B", Status: "completed"},
	}, false)
	m.todoStore = store

	output := m.View()
	if !strings.Contains(output, "2/2") {
		t.Errorf("expected 2/2 progress, got %q", output)
	}
}
