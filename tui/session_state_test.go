package tui

import (
	"testing"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/goal"
	"github.com/yusiwen/tinycode/session"
	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/types"
)

// seedBranch writes one session with two turns and returns its id.
func seedBranch(t *testing.T, dir, id, user, assistant string) {
	t.Helper()
	s := session.New(id, dir)
	if err := s.Append(types.Message{Role: types.RoleUser, Content: user}); err != nil {
		t.Fatal(err)
	}
	if err := s.Append(types.Message{Role: types.RoleAssistant, Content: assistant}); err != nil {
		t.Fatal(err)
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
}

// TestSessionSwitchDoesNotLeakTodos is the report behind issue #270: switching to
// another session left the previous session's todo list in place, so its items
// were snapshotted into the new session's first message and injected into the new
// session's model context.
func TestSessionSwitchDoesNotLeakTodos(t *testing.T) {
	dir := t.TempDir()
	seedBranch(t, dir, "A", "a-one", "a-two")
	seedBranch(t, dir, "B", "b-one", "b-two")

	m := newGoalTestTUI(t, dir)
	m.currentBranch = "A"
	m.messages = []chatMessage{{Role: "user", Content: "a-one"}, {Role: "assistant", Content: "a-two"}}
	if err := m.todoStore.Write([]tool.TodoItem{
		{ID: "1", Content: "one", Status: tool.StatusPending},
		{ID: "2", Content: "two", Status: tool.StatusPending},
		{ID: "3", Content: "three", Status: tool.StatusPending},
	}, false); err != nil {
		t.Fatalf("write todos: %v", err)
	}

	m.handleCommand("/session B")

	if got := m.todoStore.Read(); len(got) != 0 {
		t.Errorf("session B inherited session A's todos: %+v", got)
	}
}

// TestSessionSwitchReplacesTheModelHistory is the same defect one layer down:
// the model's context is session state too, and it was not swapped either.
func TestSessionSwitchReplacesTheModelHistory(t *testing.T) {
	dir := t.TempDir()
	seedBranch(t, dir, "A", "a-one", "a-two")
	seedBranch(t, dir, "B", "b-one", "b-two")

	m := newGoalTestTUI(t, dir)
	m.currentBranch = "A"
	m.messages = []chatMessage{{Role: "user", Content: "a-one"}, {Role: "assistant", Content: "a-two"}}
	m.agent.History = []types.Message{
		{Role: types.RoleUser, Content: "a-one"},
		{Role: types.RoleAssistant, Content: "a-two"},
	}

	m.handleCommand("/session B")

	for _, msg := range m.agent.History {
		if msg.Content == "a-one" || msg.Content == "a-two" {
			t.Fatalf("session A's history survived the switch to B: %+v", m.agent.History)
		}
	}
	want := []string{"b-one", "b-two"}
	if len(m.agent.History) != len(want) {
		t.Fatalf("history has %d turns, want %d: %+v", len(m.agent.History), len(want), m.agent.History)
	}
	for i, content := range want {
		if m.agent.History[i].Content != content {
			t.Errorf("history[%d] = %q, want %q", i, m.agent.History[i].Content, content)
		}
	}
}

// todosFixture is a small list for the assertions below.
func todosFixture(status string) []types.TodoItem {
	return []types.TodoItem{
		{ID: "1", Content: "one", Status: status},
		{ID: "2", Content: "two", Status: status},
		{ID: "3", Content: "three", Status: status},
	}
}

// TestResumeRestoresTheTodoList: the list is session state, so --resume has to
// bring it back from the file rather than from whatever this process had.
func TestResumeRestoresTheTodoList(t *testing.T) {
	dir := t.TempDir()
	s := session.New("TUI-resume-todos", dir)
	s.Append(types.Message{Role: types.RoleUser, Content: "work"})
	s.Todos = todosFixture(tool.StatusPending)
	if err := s.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	m := newGoalTestTUI(t, dir, "TUI-resume-todos")
	if got := m.todoStore.Read(); len(got) != 3 {
		t.Fatalf("restored %d todos, want 3: %+v", len(got), got)
	}
	for i, item := range m.todoStore.Read() {
		if item.Content != todosFixture("")[i].Content {
			t.Errorf("todo %d = %q", i, item.Content)
		}
	}
}

// TestResumeWithNoTodosClearsTheStore is the leak the other way round: a process
// that already had a list must not carry it into a session that never had one.
func TestResumeWithNoTodosClearsTheStore(t *testing.T) {
	dir := t.TempDir()
	seedBranch(t, dir, "TUI-empty-todos", "hello", "hi")

	store := tool.NewTodoStore()
	if err := store.Write(todosFixture(tool.StatusPending), false); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	provider := answerProvider("done")
	m := NewTUI(agent.New(provider), &config.Config{SessionDir: dir}, agent.NewRegistry(),
		agent.NewProviderRegistry([]agent.ProviderRecord{{Name: "test", Provider: provider}}),
		store, nil, "TUI-empty-todos")

	if got := m.todoStore.Read(); len(got) != 0 {
		t.Errorf("a session with no list kept the process's previous one: %+v", got)
	}
}

// TestSessionSwitchRestoresTheTargetTodos: each session brings its own list, in
// both directions.
func TestSessionSwitchRestoresTheTargetTodos(t *testing.T) {
	dir := t.TempDir()
	a := session.New("A", dir)
	a.Append(types.Message{Role: types.RoleUser, Content: "a-one"})
	a.Todos = todosFixture(tool.StatusCompleted)
	if err := a.Flush(); err != nil {
		t.Fatalf("flush A: %v", err)
	}
	b := session.New("B", dir)
	b.Append(types.Message{Role: types.RoleUser, Content: "b-one"})
	b.Todos = []types.TodoItem{{ID: "9", Content: "b-task", Status: tool.StatusPending}}
	if err := b.Flush(); err != nil {
		t.Fatalf("flush B: %v", err)
	}

	m := newGoalTestTUI(t, dir)
	m.handleCommand("/session A")
	if got := m.todoStore.Read(); len(got) != 3 {
		t.Fatalf("session A shows %d todos, want 3", len(got))
	}

	m.handleCommand("/session B")
	got := m.todoStore.Read()
	if len(got) != 1 || got[0].Content != "b-task" {
		t.Fatalf("session B shows %+v, want its own one item", got)
	}

	m.handleCommand("/session A")
	if got := m.todoStore.Read(); len(got) != 3 || got[0].Status != tool.StatusCompleted {
		t.Errorf("switching back lost A's list: %+v", got)
	}
}

// TestForkCarriesTodosAndADisarmedGoal: a branch is the same work, so it inherits
// the plan and the goal — and a goal never arrives armed from a file.
func TestForkCarriesTodosAndADisarmedGoal(t *testing.T) {
	dir := t.TempDir()
	parent := session.New("parent", dir)
	parent.Append(types.Message{Role: types.RoleUser, Content: "one"})
	parent.Append(types.Message{Role: types.RoleAssistant, Content: "two"})
	parent.Todos = todosFixture(tool.StatusPending)
	parent.Goal = &goal.Goal{
		ID: "goal-parent", Objective: "make it pass", Phase: goal.PhaseActive,
		MaxGoalRounds: 8, Revision: 1,
	}
	if err := parent.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	m := newGoalTestTUI(t, dir, "parent")
	if !m.goals.Armed() {
		// Restored goals are disarmed; arm it so the fork has something to disarm.
		if _, err := m.goals.Resume(m.goals.Snapshot().Ref()); err != nil {
			t.Fatalf("arm the parent goal: %v", err)
		}
	}

	m.handleCommand("/fork")

	if got := m.todoStore.Read(); len(got) != 3 {
		t.Errorf("the branch has %d todos, want the parent's 3", len(got))
	}
	if got := m.goals.Snapshot(); got == nil || got.ID != "goal-parent" {
		t.Fatalf("the branch lost the goal: %+v", got)
	}
	if m.goals.Armed() {
		t.Error("the branch's goal is armed; a fork must come back disarmed")
	}
}
