package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/goal"
	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/types"
)

// zeroGrace removes the wait before an automatic round for the duration of one
// test. The wait exists so a human who was already typing gets in first; a test
// that keeps it would take seconds per round and prove nothing extra.
func zeroGrace(t *testing.T) {
	t.Helper()
	old := goalIdleGrace
	goalIdleGrace = 0
	t.Cleanup(func() { goalIdleGrace = old })
}

// driverTestTUI builds a TUI over dir whose provider is the one given, so a test
// can record what the model was told and how.
func driverTestTUI(t *testing.T, dir string, provider *agent.MockProvider) *TuiModel {
	t.Helper()
	return NewTUI(agent.New(provider), &config.Config{SessionDir: dir}, agent.NewRegistry(),
		agent.NewProviderRegistry([]agent.ProviderRecord{{Name: "test", Provider: provider}}),
		tool.NewTodoStore(), nil)
}

func answerProvider(answer string) *agent.MockProvider {
	return &agent.MockProvider{ChatFunc: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
		return &types.ChatResponse{Content: answer}, nil
	}}
}

// fireRound executes the driver's scheduled command and feeds the message back,
// returning the new model.
func fireRound(t *testing.T, m *TuiModel, cmd tea.Cmd) *TuiModel {
	t.Helper()
	if cmd == nil {
		t.Fatal("the driver scheduled no round")
	}
	msg := cmd()
	if _, ok := msg.(goalRoundMsg); !ok {
		t.Fatalf("the scheduled command produced %T, want goalRoundMsg", msg)
	}
	model, _ := m.Update(msg)
	return model.(*TuiModel)
}

func lastUserContent(m *TuiModel) string {
	for i := len(m.messages) - 1; i >= 0; i-- {
		if m.messages[i].Role == "user" {
			return m.messages[i].Content
		}
	}
	return ""
}

// TestGoalRoundStartsAfterIdle is the core of D7: a goal a human created starts a
// round once the session is idle, and the round is counted, visible and marked.
func TestGoalRoundStartsAfterIdle(t *testing.T) {
	zeroGrace(t)
	var inRound int32
	provider := &agent.MockProvider{ChatFunc: func(ctx context.Context, _ types.ChatRequest) (*types.ChatResponse, error) {
		if types.InGoalRound(ctx) {
			atomic.StoreInt32(&inRound, 1)
		}
		return &types.ChatResponse{Content: "round done"}, nil
	}}
	m := driverTestTUI(t, t.TempDir(), provider)

	_, cmd := m.handleCommand("/goal make the failing test pass")
	m = fireRound(t, m, cmd)

	if m.status != StatusStreaming {
		t.Fatalf("status = %v, want streaming", m.status)
	}
	if !m.currentRunIsGoal {
		t.Error("the run is not marked as a goal round")
	}
	if got := m.goals.Snapshot().RoundsStarted; got != 1 {
		t.Errorf("rounds started = %d, want 1", got)
	}
	prompt := lastUserContent(m)
	for _, want := range []string{"<goal_round>", `Objective: "make the failing test pass"`, "Round: 1/256"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the round prompt is missing %q:\n%s", want, prompt)
		}
	}

	drainUntilTerminal(t, m, 5*time.Second)

	if atomic.LoadInt32(&inRound) != 1 {
		t.Error("the provider's context was not marked as a goal round")
	}
	if got := m.goals.Snapshot(); got.Phase != goal.PhaseActive {
		t.Errorf("phase = %s after a tool-free round, want active", got.Phase)
	}
}

// TestNoRoundWhileTheHumanIsTyping: a draft in the input box wins. This is the
// interim answer to there being no mid-run steering yet.
func TestNoRoundWhileTheHumanIsTyping(t *testing.T) {
	zeroGrace(t)
	m := driverTestTUI(t, t.TempDir(), answerProvider("done"))
	if _, err := m.goals.Create("the objective", 8); err != nil {
		t.Fatalf("create: %v", err)
	}

	m.input.SetValue("half a thought")
	if cmd := m.scheduleGoalRound(); cmd != nil {
		t.Error("the driver scheduled a round while the input box had text")
	}

	m.input.SetValue("   ")
	if cmd := m.scheduleGoalRound(); cmd == nil {
		t.Error("whitespace in the input box blocked the round")
	}
}

func TestNoRoundWhenTheGoalCannotContinue(t *testing.T) {
	zeroGrace(t)
	t.Run("no goal", func(t *testing.T) {
		m := driverTestTUI(t, t.TempDir(), answerProvider("done"))
		if cmd := m.scheduleGoalRound(); cmd != nil {
			t.Error("a round was scheduled with no goal")
		}
	})
	t.Run("paused", func(t *testing.T) {
		m := driverTestTUI(t, t.TempDir(), answerProvider("done"))
		created, _ := m.goals.Create("the objective", 8)
		if _, err := m.goals.Pause(created.Ref()); err != nil {
			t.Fatalf("pause: %v", err)
		}
		if cmd := m.scheduleGoalRound(); cmd != nil {
			t.Error("a round was scheduled for a paused goal")
		}
	})
	t.Run("disarmed", func(t *testing.T) {
		m := driverTestTUI(t, t.TempDir(), answerProvider("done"))
		created, _ := m.goals.Create("the objective", 8)
		if err := m.goals.Restore(created.Clone()); err != nil {
			t.Fatalf("restore: %v", err)
		}
		if cmd := m.scheduleGoalRound(); cmd != nil {
			t.Error("a round was scheduled for a disarmed goal")
		}
	})
	t.Run("dialog open", func(t *testing.T) {
		m := driverTestTUI(t, t.TempDir(), answerProvider("done"))
		if _, err := m.goals.Create("the objective", 8); err != nil {
			t.Fatalf("create: %v", err)
		}
		m.dialogMode = true
		if cmd := m.scheduleGoalRound(); cmd != nil {
			t.Error("a round was scheduled while a dialog was open")
		}
	})
}

// TestGoalRoundCapRecordsABlocker: spending the last round must leave a durable
// reason, not a goal that quietly stops.
func TestGoalRoundCapRecordsABlocker(t *testing.T) {
	zeroGrace(t)
	m := driverTestTUI(t, t.TempDir(), answerProvider("round done"))
	if _, err := m.goals.Create("the objective", 1); err != nil {
		t.Fatalf("create: %v", err)
	}

	m = fireRound(t, m, m.scheduleGoalRound())
	drainUntilTerminal(t, m, 5*time.Second)

	got := m.goals.Snapshot()
	if got.Phase != goal.PhaseBlocked {
		t.Fatalf("phase = %s, want blocked at the cap", got.Phase)
	}
	if got.Blocker == nil || got.Blocker.Code != goal.BlockerRoundLimit {
		t.Errorf("blocker = %+v, want the round-limit code", got.Blocker)
	}
	if !strings.Contains(lastMessage(m), "round budget") {
		t.Errorf("the user is not told why the goal stopped: %q", lastMessage(m))
	}
	if cmd := m.scheduleGoalRound(); cmd != nil {
		t.Error("a round was scheduled after the cap")
	}
}

func TestCancelledRoundPausesTheGoal(t *testing.T) {
	zeroGrace(t)
	started := make(chan struct{}, 1)
	provider := &agent.MockProvider{ChatFunc: func(ctx context.Context, _ types.ChatRequest) (*types.ChatResponse, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	m := driverTestTUI(t, t.TempDir(), provider)
	if _, err := m.goals.Create("the objective", 8); err != nil {
		t.Fatalf("create: %v", err)
	}

	m = fireRound(t, m, m.scheduleGoalRound())
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("the round never reached the provider")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	drainUntilTerminal(t, m, 5*time.Second)

	got := m.goals.Snapshot()
	if got.Phase != goal.PhasePaused {
		t.Errorf("phase = %s after a cancelled round, want paused", got.Phase)
	}
	if m.goals.Armed() {
		t.Error("a cancelled round must disarm the goal: cancellation never auto-restarts")
	}
}

func TestFailedRoundPausesTheGoal(t *testing.T) {
	zeroGrace(t)
	provider := &agent.MockProvider{ChatFunc: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
		return nil, context.DeadlineExceeded
	}}
	m := driverTestTUI(t, t.TempDir(), provider)
	if _, err := m.goals.Create("the objective", 8); err != nil {
		t.Fatalf("create: %v", err)
	}

	m = fireRound(t, m, m.scheduleGoalRound())
	drainUntilTerminal(t, m, 5*time.Second)

	if got := m.goals.Snapshot(); got.Phase != goal.PhasePaused {
		t.Errorf("phase = %s after a failed round, want paused", got.Phase)
	}
}

// TestToolFreeRoundsBlockTheGoal: a round that only narrates is a loop the cap
// cannot catch cheaply, so three of them block it.
func TestToolFreeRoundsBlockTheGoal(t *testing.T) {
	zeroGrace(t)
	m := driverTestTUI(t, t.TempDir(), answerProvider("I looked at it"))
	if _, err := m.goals.Create("the objective", 8); err != nil {
		t.Fatalf("create: %v", err)
	}

	for i := 0; i < goalNoProgressRounds; i++ {
		m = fireRound(t, m, m.scheduleGoalRound())
		drainUntilTerminal(t, m, 5*time.Second)
	}

	got := m.goals.Snapshot()
	if got.Phase != goal.PhaseBlocked {
		t.Fatalf("phase = %s, want blocked after %d tool-free rounds", got.Phase, goalNoProgressRounds)
	}
	if got.Blocker == nil || got.Blocker.Code != goal.BlockerNoProgress {
		t.Errorf("blocker = %+v, want the no-progress code", got.Blocker)
	}
	if got.RoundsStarted != goalNoProgressRounds {
		t.Errorf("rounds started = %d, want %d", got.RoundsStarted, goalNoProgressRounds)
	}
}

// TestHumanTurnLeavesTheSessionEligible: the driver does not care who ended the
// turn — after any finished turn, an active goal continues.
func TestHumanTurnLeavesTheSessionEligible(t *testing.T) {
	zeroGrace(t)
	m := driverTestTUI(t, t.TempDir(), answerProvider("human answer"))
	if _, err := m.goals.Create("the objective", 8); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, cmd := m.Update(ChatMsg{Text: "do this first"}); cmd == nil {
		t.Fatal("the human run did not start")
	}
	drainUntilTerminal(t, m, 5*time.Second)

	if cmd := m.scheduleGoalRound(); cmd == nil {
		t.Fatal("no round is eligible after a finished human turn")
	}
	m = fireRound(t, m, m.scheduleGoalRound())
	if got := m.goals.Snapshot().RoundsStarted; got != 1 {
		t.Errorf("rounds started = %d, want 1", got)
	}
	drainUntilTerminal(t, m, 5*time.Second)
}

// TestASecondRoundMessageIsRefused: the tick is advisory, so a stale one that
// arrives while a round runs must not start another.
func TestASecondRoundMessageIsRefused(t *testing.T) {
	zeroGrace(t)
	m := driverTestTUI(t, t.TempDir(), answerProvider("round done"))
	if _, err := m.goals.Create("the objective", 8); err != nil {
		t.Fatalf("create: %v", err)
	}
	m = fireRound(t, m, m.scheduleGoalRound())

	model, cmd := m.Update(goalRoundMsg{})
	m = model.(*TuiModel)
	if cmd != nil {
		t.Error("a stale round message started a run")
	}
	if got := m.goals.Snapshot().RoundsStarted; got != 1 {
		t.Errorf("rounds started = %d, want 1", got)
	}
	drainUntilTerminal(t, m, 5*time.Second)
}

// TestSettleGoalRoundReportsWhyItStopped: the run-end path turns this string into
// a completion announcement, so it has to name what happened rather than say
// "finished" over a goal that just spent its budget.
func TestSettleGoalRoundReportsWhyItStopped(t *testing.T) {
	t.Run("round budget spent", func(t *testing.T) {
		m := driverTestTUI(t, t.TempDir(), answerProvider("done"))
		if _, err := m.goals.Create("the objective", 1); err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := m.goals.StartRound(); err != nil {
			t.Fatalf("start round: %v", err)
		}
		m.runGoalPhase = goal.PhaseActive

		notice := m.settleGoalRound(nil)
		if notice != "goal blocked: round budget spent" {
			t.Errorf("notice = %q", notice)
		}
		if got := m.goals.Snapshot().Blocker; got == nil || got.Code != goal.BlockerRoundLimit {
			t.Errorf("blocker = %+v, want round-limit", got)
		}
	})

	t.Run("the round failed", func(t *testing.T) {
		m := driverTestTUI(t, t.TempDir(), answerProvider("done"))
		if _, err := m.goals.Create("the objective", 8); err != nil {
			t.Fatalf("create: %v", err)
		}
		m.currentRunIsGoal = true
		m.runGoalPhase = goal.PhaseActive

		notice := m.settleGoalRound(context.DeadlineExceeded)
		if notice != "goal paused: the round ended with an error" {
			t.Errorf("notice = %q", notice)
		}
		if got := m.goals.Snapshot().Phase; got != goal.PhasePaused {
			t.Errorf("phase = %s, want paused", got)
		}
	})

	t.Run("the model completed it", func(t *testing.T) {
		m := driverTestTUI(t, t.TempDir(), answerProvider("done"))
		created, err := m.goals.Create("the objective", 8)
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := m.goals.Complete(created.Ref()); err != nil {
			t.Fatalf("complete: %v", err)
		}
		m.runGoalPhase = goal.PhaseActive

		if notice := m.settleGoalRound(nil); notice != "goal complete" {
			t.Errorf("notice = %q", notice)
		}
		// A later run of the same completed goal must not announce it again.
		m.runGoalPhase = goal.PhaseComplete
		if notice := m.settleGoalRound(nil); notice != "" {
			t.Errorf("a completed goal announced again: %q", notice)
		}
	})
}
