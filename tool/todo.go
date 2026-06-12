package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusCancelled  = "cancelled"

	MaxTodoItems        = 256
	MaxTodoContentChars = 4000
)

// TodoItem represents a single tracked task.
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}

// TodoSummary provides a quick overview of the task list.
type TodoSummary struct {
	Total      int `json:"total"`
	Pending    int `json:"pending"`
	InProgress int `json:"in_progress"`
	Completed  int `json:"completed"`
	Cancelled  int `json:"cancelled"`
}

	// Todo result type for JSON serialization
type TodoResult struct {
	Todos   []TodoItem  `json:"todos"`
	Summary TodoSummary `json:"summary"`
}

// TodoStore is an in-memory ordered list of task items.
// Only ONE item may be in_progress at a time.
type TodoStore struct {
	items []TodoItem
}

// NewTodoStore creates an empty store.
func NewTodoStore() *TodoStore {
	return &TodoStore{}
}

// Write replaces or merges todo items. When merge=false, replaces the entire list.
// When merge=true, updates existing items by ID and appends new ones.
func (s *TodoStore) Write(todos []TodoItem, merge bool) error {
	if len(todos) > MaxTodoItems {
		return fmt.Errorf("too many todos: %d (max %d)", len(todos), MaxTodoItems)
	}
	for _, t := range todos {
		if len(t.Content) > MaxTodoContentChars {
			return fmt.Errorf("todo content too long: %d chars (max %d)", len(t.Content), MaxTodoContentChars)
		}
	}

	if !merge {
		s.items = make([]TodoItem, len(todos))
		copy(s.items, todos)
	} else {
		for _, src := range todos {
			found := false
			for i, dst := range s.items {
				if dst.ID == src.ID {
					s.items[i].Status = src.Status
					if src.Content != "" {
						s.items[i].Content = src.Content
					}
					found = true
					break
				}
			}
			if !found {
				s.items = append(s.items, src)
			}
		}
	}

	// Enforce: only one in_progress
	hasInProgress := false
	for i := range s.items {
		if s.items[i].Status == StatusInProgress {
			if hasInProgress {
				s.items[i].Status = StatusPending
			}
			hasInProgress = true
		}
	}

	return nil
}

// Read returns a copy of the current todo list.
func (s *TodoStore) Read() []TodoItem {
	cp := make([]TodoItem, len(s.items))
	copy(cp, s.items)
	return cp
}

// Summary calculates counts for each status.
func (s *TodoStore) Summary() TodoSummary {
	var sum TodoSummary
	for _, t := range s.items {
		sum.Total++
		switch t.Status {
		case StatusPending:
			sum.Pending++
		case StatusInProgress:
			sum.InProgress++
		case StatusCompleted:
			sum.Completed++
		case StatusCancelled:
			sum.Cancelled++
		}
	}
	return sum
}

// FormatForInjection returns a compact string of active items (pending + in_progress)
// for re-injection after context compression.
func (s *TodoStore) FormatForInjection() string {
	var b strings.Builder
	for _, t := range s.items {
		if t.Status == StatusCompleted || t.Status == StatusCancelled {
			continue
		}
		marker := "[ ]"
		switch t.Status {
		case StatusInProgress:
			marker = "[>]"
		case StatusCompleted:
			marker = "[x]"
		case StatusCancelled:
			marker = "[~]"
		}
		b.WriteString(fmt.Sprintf("%s %s\n", marker, t.Content))
	}
	return b.String()
}

// todoSchema returns the OpenAI function-calling schema for the todo tool.
func todoSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"todos": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id":      map[string]any{"type": "string"},
						"content": map[string]any{"type": "string"},
						"status":  map[string]any{"type": "string", "enum": []string{StatusPending, StatusInProgress, StatusCompleted, StatusCancelled}},
					},
				},
				"description": "Task items to write. Omit to read current list.",
			},
			"merge": map[string]any{
				"type":        "boolean",
				"description": "true: update existing items by id, add new ones. false (default): replace the entire list.",
			},
		},
	}
}

// Todo returns a Tool that manages the task list via a shared TodoStore.
// The store must be set on the Tool's Store field before use.
func Todo(store *TodoStore) Tool {
	return Tool{
		Name:        "todo",
		Description: "Manage task list. Use the todos array to create or update tasks. " +
			"Only ONE item in_progress at a time. " +
			"Mark items completed immediately when done. " +
			"If something fails, cancel it and add a revised item. " +
			"Returns JSON with todos and summary.",
		Parameters:  todoSchema(),
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			var todos []TodoItem
			merge := false

			if raw, ok := args["todos"]; ok {
				b, err := json.Marshal(raw)
				if err != nil {
					return "", fmt.Errorf("parse todos: %w", err)
				}
				if err := json.Unmarshal(b, &todos); err != nil {
					return "", fmt.Errorf("unmarshal todos: %w", err)
				}
			}
			if m, ok := args["merge"]; ok {
				if b, ok := m.(bool); ok {
					merge = b
				}
			}

			if todos != nil {
				if err := store.Write(todos, merge); err != nil {
					return "", err
				}
			}

			summary := store.Summary()
			result := TodoResult{
				Todos:   store.Read(),
				Summary: summary,
			}
			b, err := json.Marshal(result)
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
	}
}
