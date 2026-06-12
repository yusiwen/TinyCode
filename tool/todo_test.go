package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func newTestStore() *TodoStore {
	return NewTodoStore()
}

func TestTodoCreate(t *testing.T) {
	store := newTestStore()
	tool := Todo(store)

	// Read empty list first
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	var res TodoResult
	json.Unmarshal([]byte(result), &res)
	if res.Summary.Total != 0 {
		t.Errorf("expected 0 items, got %d", res.Summary.Total)
	}

	// Create 2 tasks
	tasks := []TodoItem{
		{ID: "1", Content: "Task one", Status: StatusInProgress},
		{ID: "2", Content: "Task two", Status: StatusPending},
	}
	b, _ := json.Marshal(tasks)
	result, err = tool.Execute(context.Background(), map[string]any{
		"todos": json.RawMessage(b),
	})
	if err != nil {
		t.Fatalf("create error: %v", err)
	}
	json.Unmarshal([]byte(result), &res)
	if res.Summary.Total != 2 {
		t.Errorf("expected 2 items, got %d", res.Summary.Total)
	}
	if res.Summary.InProgress != 1 {
		t.Errorf("expected 1 in_progress, got %d", res.Summary.InProgress)
	}
}

func TestTodoMerge(t *testing.T) {
	store := newTestStore()
	// Initialize with 2 items
	store.Write([]TodoItem{
		{ID: "1", Content: "First", Status: StatusInProgress},
		{ID: "2", Content: "Second", Status: StatusPending},
	}, false)

	// Merge: complete task 1, add task 3
	tool := Todo(store)
	tasks := []TodoItem{
		{ID: "1", Status: StatusCompleted},
		{ID: "3", Content: "Third", Status: StatusPending},
	}
	b, _ := json.Marshal(tasks)
	result, err := tool.Execute(context.Background(), map[string]any{
		"todos": json.RawMessage(b),
		"merge": true,
	})
	if err != nil {
		t.Fatalf("merge error: %v", err)
	}
	var res TodoResult
	json.Unmarshal([]byte(result), &res)
	if res.Summary.Total != 3 {
		t.Errorf("expected 3 items after merge, got %d", res.Summary.Total)
	}
	if res.Summary.Completed != 1 {
		t.Errorf("expected 1 completed, got %d", res.Summary.Completed)
	}
}

func TestTodoOnlyOneInProgress(t *testing.T) {
	store := newTestStore()
	err := store.Write([]TodoItem{
		{ID: "1", Content: "A", Status: StatusInProgress},
		{ID: "2", Content: "B", Status: StatusInProgress},
	}, false)
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	summary := store.Summary()
	if summary.InProgress != 1 {
		t.Errorf("expected exactly 1 in_progress after dedup, got %d", summary.InProgress)
	}
}

func TestTodoMaxItems(t *testing.T) {
	store := newTestStore()
	items := make([]TodoItem, MaxTodoItems+1)
	for i := range items {
		items[i] = TodoItem{ID: string(rune('A' + i)), Content: "x", Status: StatusPending}
	}
	err := store.Write(items, false)
	if err == nil {
		t.Fatal("expected error for exceeding max items")
	}
	if !strings.Contains(err.Error(), "too many") {
		t.Errorf("expected 'too many' error, got %q", err)
	}
}

func TestTodoContentMaxChars(t *testing.T) {
	store := newTestStore()
	long := strings.Repeat("x", MaxTodoContentChars+1)
	err := store.Write([]TodoItem{{ID: "1", Content: long, Status: StatusPending}}, false)
	if err == nil {
		t.Fatal("expected error for exceeding max content chars")
	}
}

func TestTodoCancel(t *testing.T) {
	store := newTestStore()
	store.Write([]TodoItem{
		{ID: "1", Content: "Do something", Status: StatusInProgress},
	}, false)

	tool := Todo(store)
	tasks := []TodoItem{
		{ID: "1", Status: StatusCancelled},
	}
	b, _ := json.Marshal(tasks)
	result, err := tool.Execute(context.Background(), map[string]any{
		"todos": json.RawMessage(b),
		"merge": true,
	})
	if err != nil {
		t.Fatalf("cancel error: %v", err)
	}
	var res TodoResult
	json.Unmarshal([]byte(result), &res)
	if res.Summary.Cancelled != 1 {
		t.Errorf("expected 1 cancelled, got %d", res.Summary.Cancelled)
	}
}

func TestTodoNoneTodos(t *testing.T) {
	store := newTestStore()
	store.Write([]TodoItem{
		{ID: "1", Content: "Existing", Status: StatusCompleted},
	}, false)

	// Read without passing todos
	tool := Todo(store)
	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	var res TodoResult
	json.Unmarshal([]byte(result), &res)
	if res.Summary.Total != 1 {
		t.Errorf("expected 1 item on read, got %d", res.Summary.Total)
	}
}

func TestFormatForInjection(t *testing.T) {
	store := newTestStore()
	store.Write([]TodoItem{
		{ID: "1", Content: "Done task", Status: StatusCompleted},
		{ID: "2", Content: "Active task", Status: StatusInProgress},
		{ID: "3", Content: "Pending task", Status: StatusPending},
	}, false)

	f := store.FormatForInjection()
	if strings.Contains(f, "Done") {
		t.Error("expected completed tasks excluded from injection")
	}
	if !strings.Contains(f, "[>]") {
		t.Error("expected in_progress marker [>] in injection")
	}
	if !strings.Contains(f, "[ ]") {
		t.Error("expected pending marker [ ] in injection")
	}
}

func TestTodoEmpty(t *testing.T) {
	store := newTestStore()
	tool := Todo(store)

	result, err := tool.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	var res TodoResult
	json.Unmarshal([]byte(result), &res)
	if res.Summary.Total != 0 {
		t.Errorf("expected 0 total, got %d", res.Summary.Total)
	}
}

func TestTodoSummary(t *testing.T) {
	store := newTestStore()
	store.Write([]TodoItem{
		{ID: "1", Content: "A", Status: StatusCompleted},
		{ID: "2", Content: "B", Status: StatusInProgress},
		{ID: "3", Content: "C", Status: StatusPending},
		{ID: "4", Content: "D", Status: StatusCancelled},
	}, false)

	s := store.Summary()
	if s.Total != 4 || s.Completed != 1 || s.InProgress != 1 || s.Pending != 1 || s.Cancelled != 1 {
		t.Errorf("unexpected summary: %+v", s)
	}
}
