package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/tool"
)

func TestTodoRecoveryFromHistory(t *testing.T) {
	store := tool.NewTodoStore()
	store.Write([]tool.TodoItem{
		{ID: "1", Content: "Existing task", Status: "completed"},
	}, false)

	m := newTestTUI()
	m.ready = true
	m.todoStore = store

	// Add a simulated todo result to messages
	todoJSON, _ := json.Marshal(tool.TodoResult{
		Todos: []tool.TodoItem{
			{ID: "1", Content: "Restored task one", Status: "completed"},
			{ID: "2", Content: "Restored task two", Status: "in_progress"},
		},
		Summary: tool.TodoSummary{Total: 2},
	})
	m.messages = append(m.messages, chatMessage{
		Role:    "assistant",
		Content: string(todoJSON),
	})

	// Simulate recovery logic
	for i := len(m.messages) - 1; i >= 0; i-- {
		msg := m.messages[i]
		if msg.Role == "assistant" && strings.Contains(msg.Content, "\"todos\"") {
			var result tool.TodoResult
			if err := json.Unmarshal([]byte(msg.Content), &result); err == nil && len(result.Todos) > 0 {
				m.todoStore.Write(result.Todos, false)
				break
			}
		}
	}

	items := m.todoStore.Read()
	if len(items) != 2 {
		t.Fatalf("expected 2 items after recovery, got %d", len(items))
	}
	if items[0].Content != "Restored task one" {
		t.Errorf("expected 'Restored task one', got %q", items[0].Content)
	}
	if items[1].Status != "in_progress" {
		t.Errorf("expected in_progress status, got %q", items[1].Status)
	}
}

func TestTodoRecoveryNoTodoMessages(t *testing.T) {
	store := tool.NewTodoStore()
	m := newTestTUI()
	m.ready = true
	m.todoStore = store

	// Messages without todo content
	m.messages = append(m.messages,
		chatMessage{Role: "user", Content: "Hello"},
		chatMessage{Role: "assistant", Content: "Hi there!"},
	)

	// Run recovery logic
	for i := len(m.messages) - 1; i >= 0; i-- {
		msg := m.messages[i]
		if msg.Role == "assistant" && strings.Contains(msg.Content, "\"todos\"") {
			var result tool.TodoResult
			if err := json.Unmarshal([]byte(msg.Content), &result); err == nil && len(result.Todos) > 0 {
				m.todoStore.Write(result.Todos, false)
				break
			}
		}
	}

	items := m.todoStore.Read()
	if len(items) != 0 {
		t.Errorf("expected 0 items without todo history, got %d", len(items))
	}
}

func TestTodoRecoveryOnlyLastResult(t *testing.T) {
	store := tool.NewTodoStore()
	m := newTestTUI()
	m.ready = true
	m.todoStore = store

	// First todo result (older)
	todo1, _ := json.Marshal(tool.TodoResult{
		Todos: []tool.TodoItem{
			{ID: "1", Content: "Old task", Status: "completed"},
		},
	})
	m.messages = append(m.messages, chatMessage{Role: "assistant", Content: string(todo1)})

	// Second todo result (newer, should be used)
	todo2, _ := json.Marshal(tool.TodoResult{
		Todos: []tool.TodoItem{
			{ID: "2", Content: "New task", Status: "in_progress"},
		},
	})
	m.messages = append(m.messages, chatMessage{Role: "assistant", Content: string(todo2)})

	// Run recovery logic
	for i := len(m.messages) - 1; i >= 0; i-- {
		msg := m.messages[i]
		if msg.Role == "assistant" && strings.Contains(msg.Content, "\"todos\"") {
			var result tool.TodoResult
			if err := json.Unmarshal([]byte(msg.Content), &result); err == nil && len(result.Todos) > 0 {
				m.todoStore.Write(result.Todos, false)
				break
			}
		}
	}

	items := m.todoStore.Read()
	if len(items) != 1 || items[0].Content != "New task" {
		t.Errorf("expected only newest todo result, got %v", items)
	}
}
