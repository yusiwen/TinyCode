package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
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

// TestTodoStoreConcurrentAccess exercises Write/Read/Summary/FormatForInjection
// from many goroutines at once. It must be clean under `go test -race` and no
// write may be lost.
func TestTodoStoreConcurrentAccess(t *testing.T) {
	store := NewTodoStore()
	if err := store.Write([]TodoItem{
		{ID: "seed", Content: "seed", Status: StatusInProgress},
	}, false); err != nil {
		t.Fatalf("seed write: %v", err)
	}

	const writers = 64
	var wg sync.WaitGroup

	// Writers merge unique items (append path); readers hit every read path.
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("w-%d", i)
			if err := store.Write([]TodoItem{
				{ID: id, Content: "task " + id, Status: StatusPending},
			}, true); err != nil {
				t.Errorf("write %s: %v", id, err)
			}
		}(i)
	}
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				items := store.Read()
				for _, it := range items {
					_ = it.Content // reading the returned slice must be race-free
				}
				_ = store.Summary()
				_ = store.FormatForInjection()
			}
		}()
	}
	wg.Wait()

	items := store.Read()
	if len(items) != writers+1 {
		t.Fatalf("expected %d items, got %d (items were lost)", writers+1, len(items))
	}
	seen := make(map[string]bool, len(items))
	for _, it := range items {
		if seen[it.ID] {
			t.Fatalf("duplicate item id %q", it.ID)
		}
		seen[it.ID] = true
	}
	for i := 0; i < writers; i++ {
		id := fmt.Sprintf("w-%d", i)
		if !seen[id] {
			t.Fatalf("missing item %q after concurrent writes", id)
		}
	}
	if s := store.Summary(); s.Total != writers+1 || s.InProgress != 1 {
		t.Fatalf("unexpected summary after concurrent writes: %+v", s)
	}
}

// TestTodoStoreReadReturnsCopy verifies callers cannot mutate store state
// through the slice returned by Read.
func TestTodoStoreReadReturnsCopy(t *testing.T) {
	store := NewTodoStore()
	if err := store.Write([]TodoItem{
		{ID: "1", Content: "original", Status: StatusPending},
	}, false); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := store.Read()
	if len(got) != 1 {
		t.Fatalf("expected 1 item, got %d", len(got))
	}
	// Mutate every possible way: element fields and slice length.
	got[0].Content = "mutated"
	got[0].Status = StatusCompleted
	got = append(got, TodoItem{ID: "ghost", Content: "ghost", Status: StatusPending})
	if len(got) != 2 {
		t.Fatalf("append did not extend the returned slice: %d", len(got))
	}

	again := store.Read()
	if len(again) != 1 {
		t.Fatalf("store length changed through returned slice: %d", len(again))
	}
	if again[0].Content != "original" || again[0].Status != StatusPending {
		t.Fatalf("store mutated through returned slice: %+v", again[0])
	}
}

// TestTodoStoreWriteCopiesInput verifies a replace-write snapshots the caller's
// slice instead of aliasing it.
func TestTodoStoreWriteCopiesInput(t *testing.T) {
	store := NewTodoStore()
	src := []TodoItem{{ID: "1", Content: "first", Status: StatusPending}}
	if err := store.Write(src, false); err != nil {
		t.Fatalf("write: %v", err)
	}

	src[0].Content = "changed"
	src[0].Status = StatusCompleted

	if got := store.Read(); got[0].Content != "first" || got[0].Status != StatusPending {
		t.Fatalf("store changed through caller slice: %+v", got[0])
	}
}
