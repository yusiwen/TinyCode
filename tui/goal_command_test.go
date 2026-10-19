package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/session"
	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/types"
)

// newGoalTestTUI builds a TUI with a session directory, so every assertion can
// be checked against the file the command actually wrote.
func newGoalTestTUI(t *testing.T, dir string, resume ...string) *TuiModel {
	t.Helper()
	provider := &agent.MockProvider{ChatFunc: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
		return &types.ChatResponse{Content: "done"}, nil
	}}
	ag := agent.New(provider)
	return NewTUI(ag, &config.Config{SessionDir: dir}, agent.NewRegistry(),
		agent.NewProviderRegistry([]agent.ProviderRecord{{Name: "test", Provider: provider}}),
		tool.NewTodoStore(), resume...)
}

// runOnce drives one complete run through the Update loop and waits for the
// terminal StreamDone, which is the point the session is persisted.
func runOnce(t *testing.T, m *TuiModel) {
	t.Helper()
	if _, cmd := m.Update(ChatMsg{Text: "do something"}); cmd == nil {
		t.Fatal("expected a stream command after ChatMsg")
	}
	drainUntilTerminal(t, m, 5*time.Second)
}

// latestSession loads the newest session file written under dir.
func latestSession(t *testing.T, dir string) *session.Session {
	t.Helper()
	store := session.NewStore(dir)
	infos := store.List()
	if len(infos) == 0 {
		t.Fatalf("no session file was written under %s", dir)
	}
	s, err := store.Load(infos[len(infos)-1].ID)
	if err != nil {
		t.Fatalf("load %q: %v", infos[len(infos)-1].ID, err)
	}
	return s
}

func lastMessage(m *TuiModel) string {
	if len(m.messages) == 0 {
		return ""
	}
	return m.messages[len(m.messages)-1].Content
}

func TestGoalCommandCreatesAndPersists(t *testing.T) {
	dir := t.TempDir()
	m := newGoalTestTUI(t, dir)

	m.handleCommand("/goal make the failing test pass")

	for _, want := range []string{"Goal: make the failing test pass", "Round: 0/256", "armed"} {
		if !strings.Contains(lastMessage(m), want) {
			t.Errorf("the /goal output is missing %q:\n%s", want, lastMessage(m))
		}
	}
	if !m.goals.Armed() {
		t.Error("a goal created by a human must be armed")
	}

	saved := latestSession(t, dir)
	if saved.Goal == nil {
		t.Fatal("the goal was not written with the session")
	}
	if saved.Goal.Objective != "make the failing test pass" || saved.Goal.Phase != "active" {
		t.Errorf("persisted goal = %+v", saved.Goal)
	}
}

func TestGoalCommandPauseResumeClear(t *testing.T) {
	dir := t.TempDir()
	m := newGoalTestTUI(t, dir)
	m.handleCommand("/goal the objective")

	m.handleCommand("/goal pause")
	if !strings.Contains(lastMessage(m), "Phase: paused") || m.goals.Armed() {
		t.Errorf("pause: %q (armed=%v)", lastMessage(m), m.goals.Armed())
	}
	if got := latestSession(t, dir).Goal; got == nil || got.Phase != "paused" {
		t.Errorf("the paused phase was not persisted: %+v", got)
	}

	m.handleCommand("/goal resume")
	if !strings.Contains(lastMessage(m), "Phase: active, armed") || !m.goals.Armed() {
		t.Errorf("resume: %q (armed=%v)", lastMessage(m), m.goals.Armed())
	}

	m.handleCommand("/goal clear")
	if !strings.Contains(lastMessage(m), "No goal is current") || m.goals.Snapshot() != nil {
		t.Errorf("clear: %q", lastMessage(m))
	}
	if got := latestSession(t, dir).Goal; got != nil {
		t.Errorf("the cleared goal is still in the session: %+v", got)
	}
}

func TestGoalCommandEditKeepsThePhase(t *testing.T) {
	dir := t.TempDir()
	m := newGoalTestTUI(t, dir)
	m.handleCommand("/goal the first objective")
	m.handleCommand("/goal pause")

	m.handleCommand("/goal edit the second objective")

	if got := m.goals.Snapshot(); got == nil || got.Objective != "the second objective" || got.Phase != "paused" {
		t.Errorf("edit changed more than the objective: %+v", got)
	}
	if !strings.Contains(lastMessage(m), "Goal: the second objective") {
		t.Errorf("the /goal output does not show the edit: %q", lastMessage(m))
	}
}

// TestGoalControlWordNeedsTheWholeInput pins the grammar: "/goal pause after
// verification" is an objective, not a pause plus discarded text.
func TestGoalControlWordNeedsTheWholeInput(t *testing.T) {
	dir := t.TempDir()
	m := newGoalTestTUI(t, dir)

	m.handleCommand("/goal pause after verification")

	got := m.goals.Snapshot()
	if got == nil {
		t.Fatal("the command created no goal")
	}
	if got.Objective != "pause after verification" || got.Phase != "active" {
		t.Errorf("goal = %+v, want the literal objective and an active phase", got)
	}
}

func TestGoalCommandWithoutAGoal(t *testing.T) {
	dir := t.TempDir()
	m := newGoalTestTUI(t, dir)

	m.handleCommand("/goal")
	if !strings.Contains(lastMessage(m), "No goal is current") {
		t.Errorf("/goal with no goal: %q", lastMessage(m))
	}
	m.handleCommand("/goal edit something")
	if !strings.Contains(lastMessage(m), "no goal is current") {
		t.Errorf("/goal edit with no goal: %q", lastMessage(m))
	}
	m.handleCommand("/goal pause")
	if !strings.Contains(lastMessage(m), "no goal is current") {
		t.Errorf("/goal pause with no goal: %q", lastMessage(m))
	}
}

// TestGoalRefusalLeavesTheGoalAlone: a refused command reports the reason the
// domain gave and changes nothing, including the revision.
func TestGoalRefusalLeavesTheGoalAlone(t *testing.T) {
	dir := t.TempDir()
	m := newGoalTestTUI(t, dir)
	m.handleCommand("/goal the objective")
	m.handleCommand("/goal pause")
	before := m.goals.Snapshot()

	m.handleCommand("/goal pause")

	if !strings.Contains(lastMessage(m), "paused, not active") && !strings.Contains(lastMessage(m), "goal is paused") {
		t.Errorf("the refusal does not name the phase: %q", lastMessage(m))
	}
	after := m.goals.Snapshot()
	if after.Phase != before.Phase || after.Revision != before.Revision {
		t.Errorf("a refused pause changed the goal: %+v -> %+v", before, after)
	}
}

// TestGoalResumesDisarmed is the property the PTY scenario also asserts, at the
// level where it can be stated directly: a session reload restores the goal and
// keeps it disarmed until a human says resume.
func TestGoalResumesDisarmed(t *testing.T) {
	dir := t.TempDir()
	first := newGoalTestTUI(t, dir)
	first.handleCommand("/goal survive a restart")
	id := first.currentBranch
	if id == "" {
		t.Fatal("the first TUI has no session id after persisting")
	}

	second := newGoalTestTUI(t, dir, id)
	restored := second.goals.Snapshot()
	if restored == nil {
		t.Fatal("the resumed session lost the goal")
	}
	if restored.Objective != "survive a restart" {
		t.Errorf("restored objective = %q", restored.Objective)
	}
	if second.goals.Armed() {
		t.Fatal("a resumed goal must be disarmed")
	}

	second.handleCommand("/goal")
	if !strings.Contains(lastMessage(second), "disarmed") || !strings.Contains(lastMessage(second), "/goal resume") {
		t.Errorf("the resumed /goal output does not say it is disarmed: %q", lastMessage(second))
	}

	second.handleCommand("/goal resume")
	if !second.goals.Armed() {
		t.Error("a human resume did not arm the goal")
	}
}

// TestSessionIsPersistedAtRunEnd covers the second safe point: a finished run
// writes the session (and the goal), not only /exit. It is proved with a goal
// cleared in memory only — after the run, the file must not have it.
func TestSessionIsPersistedAtRunEnd(t *testing.T) {
	dir := t.TempDir()
	m := newGoalTestTUI(t, dir)
	m.handleCommand("/goal the objective")
	if saved := latestSession(t, dir); saved.Goal == nil {
		t.Fatal("the created goal was not persisted")
	}

	m.goals.Clear() // in memory only: no command, so nothing is written yet
	if saved := latestSession(t, dir); saved.Goal == nil {
		t.Fatal("clearing the service should not touch the file before a safe point")
	}

	runOnce(t, m)

	if saved := latestSession(t, dir); saved.Goal != nil {
		t.Errorf("the run-end save did not write the current goal state: %+v", saved.Goal)
	}
}

func TestGoalIsInThePaletteAndHelp(t *testing.T) {
	found := false
	for _, entry := range commandList() {
		if entry.Name == "/goal" {
			found = true
		}
	}
	if !found {
		t.Error("/goal is not in the command palette")
	}

	m := newTestTUI()
	m.handleCommand("/help")
	if !strings.Contains(lastMessage(m), "/goal") {
		t.Error("/help does not mention /goal")
	}
}
