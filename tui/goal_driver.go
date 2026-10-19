package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/yusiwen/tinycode/goal"
	"github.com/yusiwen/tinycode/types"
)

// goalIdleGrace is how long the session must stay idle before an automatic round
// starts. It is a variable so tests can set it to zero; production waits long
// enough for someone who was already typing to get their message in first, which
// matters because a run refuses concurrent input.
var goalIdleGrace = 2 * time.Second

// goalNoProgressRounds is how many consecutive rounds may make no tool call
// before the goal is blocked. A round that only narrates is the failure mode a
// round cap cannot catch: 256 of them would cost the cap and produce nothing.
const goalNoProgressRounds = 3

// batch composes commands, dropping the nil ones.
//
// tea.Batch panics on a nil command, and the driver's contribution is nil most of
// the time, so every path that may or may not have a goal round to schedule goes
// through here.
func batch(cmds ...tea.Cmd) tea.Cmd {
	kept := make([]tea.Cmd, 0, len(cmds))
	for _, cmd := range cmds {
		if cmd != nil {
			kept = append(kept, cmd)
		}
	}
	switch len(kept) {
	case 0:
		return nil
	case 1:
		return kept[0]
	}
	return tea.Batch(kept...)
}

// beginAgentRun admits one run and starts it. The bool is false when a run or a
// compression already owns the session, and then nothing was changed.
//
// goalRound travels to runAgent as a value rather than through shared state, so
// the context the agent sees cannot be changed by a later run.
func (m *TuiModel) beginAgentRun(prompt string, round int) (tea.Model, tea.Cmd, bool) {
	ctx, runID, ok := m.beginRun()
	if !ok {
		return m, nil, false
	}
	m.currentRunIsGoal = round > 0
	m.runGoalPhase = ""
	if current := m.goals.Snapshot(); current != nil {
		m.runGoalPhase = current.Phase
	}
	goalRun := m.goalRunInfo(round)
	m.runToolCallsStart = m.sessionToolCalls
	m.messages = append(m.messages, chatMessage{Role: "user", Content: prompt})
	cur := chatMessage{Role: "assistant", Streaming: true}
	m.messages = append(m.messages, cur)
	m.curAssistant = &m.messages[len(m.messages)-1]
	m.status = StatusStreaming
	m.streamDoneNotified = false
	m.callTokens = 0 // a new call starts with no estimate charged to it
	go m.runAgent(ctx, runID, prompt, goalRun)
	return m, m.waitForStream(), true
}

// goalRunInfo attributes the run to the session's goal, when one is current.
// A claimed round makes it a driver run; any other run is a human turn that
// carries the round count for context, not authority.
func (m *TuiModel) goalRunInfo(round int) *types.GoalRunInfo {
	if m.goals == nil {
		return nil
	}
	current := m.goals.Snapshot()
	if current == nil {
		return nil
	}
	info := &types.GoalRunInfo{GoalID: current.ID, Round: current.RoundsStarted, Source: types.GoalSourceHuman}
	if round > 0 {
		info.Round = round
		info.Source = types.GoalSourceDriver
	}
	return info
}

// goalState is what the driver needs to know about the interface.
func (m *TuiModel) goalState() goal.State {
	return goal.State{
		Idle:        m.status == StatusIdle,
		Running:     m.runIsActive(),
		Compressing: m.compressIsActive(),
		Dialog:      m.dialogMode,
		Input:       m.input.Value(),
	}
}

// scheduleGoalRound arms one tick, or returns nil when no round may start now.
//
// The decision is made twice on purpose: here, so a session that is not ready
// schedules nothing, and again in the handler when the tick arrives, because the
// user may have started typing during the grace period — human input wins.
func (m *TuiModel) scheduleGoalRound() tea.Cmd {
	if m.goals == nil {
		return nil
	}
	if decision := goal.Decide(m.goals.Snapshot(), m.goals.Armed(), m.goalState()); !decision.Continue {
		return nil
	}
	return tea.Tick(goalIdleGrace, func(time.Time) tea.Msg { return goalRoundMsg{} })
}

// startGoalRound claims the next round and starts the run.
//
// The round is counted and persisted *before* the work, so a crash between the
// claim and the end of the round wastes at most one round instead of making the
// budget unenforceable.
func (m *TuiModel) startGoalRound() (tea.Model, tea.Cmd) {
	if m.goals == nil {
		return m, nil
	}
	if decision := goal.Decide(m.goals.Snapshot(), m.goals.Armed(), m.goalState()); !decision.Continue {
		return m, nil
	}
	round, err := m.goals.StartRound()
	if err != nil {
		m.appendSystem(fmt.Sprintf("Goal round not started: %v", err))
		return m, nil
	}
	prompt := goal.RoundPrompt(m.goals.Snapshot(), round)
	m.persistSession()

	model, cmd, ok := m.beginAgentRun(prompt, round)
	if !ok {
		// Unreachable: the decision above and this call happen in one Update, so
		// nothing can take the session in between. Reported rather than hidden,
		// because the round has already been counted.
		m.appendSystem("Goal round could not start: another run took the session")
		return m, nil
	}
	m.autoScroll()
	return model, cmd
}

// settleGoalRound records what the run that just ended means for the goal.
//
// A round that failed or was cancelled pauses the goal: an automatic retry after
// a provider error is a loop nobody asked for. Consecutive rounds that made no
// tool call block it, and so does spending the last round — a goal that stopped
// for a reason has to say so, or the user finds out only by typing /goal.
// It returns a short notice when the goal stopped, which the run-end path turns
// into a completion announcement; an empty string means nothing to announce.
func (m *TuiModel) settleGoalRound(runErr error) string {
	if m.goals == nil {
		return ""
	}
	current := m.goals.Snapshot()
	if current == nil {
		return ""
	}
	ref := current.Ref()

	if m.currentRunIsGoal && runErr != nil {
		if _, err := m.goals.Pause(ref); err == nil {
			m.appendSystem("Goal paused: the round ended with an error. /goal resume re-arms it.")
			return "goal paused: the round ended with an error"
		}
		return ""
	}

	if m.currentRunIsGoal {
		if m.sessionToolCalls == m.runToolCallsStart {
			m.goalToolFreeRounds++
		} else {
			m.goalToolFreeRounds = 0
		}
		if m.goalToolFreeRounds >= goalNoProgressRounds {
			if _, err := m.goals.Block(ref, goal.BlockerNoProgress,
				fmt.Sprintf("%d consecutive rounds made no tool calls", m.goalToolFreeRounds)); err == nil {
				m.appendSystem("Goal blocked: consecutive rounds made no tool calls. Investigate, then /goal resume.")
				return "goal blocked: no progress"
			}
			return ""
		}
	}

	if current.Phase == goal.PhaseActive && current.RoundsLeft() == 0 {
		if _, err := m.goals.Block(ref, goal.BlockerRoundLimit,
			fmt.Sprintf("the goal spent its %d rounds", current.MaxGoalRounds)); err == nil {
			m.appendSystem("Goal blocked: it spent its round budget. Raise max_goal_rounds, then /goal resume.")
			return "goal blocked: round budget spent"
		}
		return ""
	}

	// The model completed it: say so once, not on every later run.
	if current.Phase == goal.PhaseComplete && m.runGoalPhase != goal.PhaseComplete {
		return "goal complete"
	}
	return ""
}
