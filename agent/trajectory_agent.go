package agent

import (
	"strings"
	"time"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// Where the loop meets the record. Everything here is a no-op when the agent has
// no Trajectory, so the loop pays one nil check per site and nothing else.

// The vocabulary a run ends in. It is small on purpose: these are the outcomes
// an evaluation harness has to tell apart, and anything finer is derivable from
// the events (a budget stop is also a run whose last llm.call is the one that
// crossed the line).
const (
	trajectoryStopFinal     = "final"
	trajectoryStopBudget    = "budget"
	trajectoryStopBlocked   = "blocked"
	trajectoryStopStepLimit = "step_limit"
	trajectoryStopCancelled = "cancelled"
	trajectoryStopError     = "error"
)

// What one provider call produced.
const (
	trajectoryOutcomeFinal     = "final"
	trajectoryOutcomeToolCalls = "tool_calls"
	trajectoryOutcomeEmpty     = "empty"
)

// runOutcome is what the loop knows about how its run ended, in the record's
// vocabulary. It is written by the loop and read by the deferred recorder.
type runOutcome struct {
	stop  string
	steps int
}

// recording says whether this agent writes anything. A delegated agent has no
// Trajectory of its own — its file is opened by the parent — so both fields
// answer the question.
func (a *Agent) recording() bool { return a.Trajectory != nil || a.TrajectoryParent != nil }

// beginTrajectory opens the run's file, or returns nil when nothing records.
func (a *Agent) beginTrajectory(prompt string, goal *types.GoalRunInfo) *TrajectoryRun {
	provider, model := splitProviderModel(a)
	agentName := ""
	if a.Config != nil {
		agentName = a.Config.Name
	}
	if a.TrajectoryParent != nil {
		// A delegated run is a child of the run that delegated it: the parent
		// numbers it and the file carries the link, which is the only way to
		// attribute a sub-agent's cost to the run that caused it.
		return a.TrajectoryParent.Child(agentName)
	}
	return a.Trajectory.Start(RunMetadata{
		Session:  a.SessionID,
		Agent:    agentName,
		Provider: provider,
		Model:    model,
		Prompt:   prompt,
		Goal:     goal,
	})
}

// finishTrajectory writes the run's last line. It runs from a defer, so it must
// not assume anything the loop did — a run that panicked or returned early still
// gets its file closed.
func (a *Agent) finishTrajectory(stop string, steps int, started time.Time, costStart types.CostTotals, unknownStart int) {
	run := a.trajRun
	a.trajRun = nil
	if run == nil {
		return
	}
	duration := time.Duration(0)
	if !started.IsZero() {
		duration = a.now().Sub(started)
	}
	run.End(stop, steps, duration, a.runUsage, costSince(a.CostTotal, costStart), a.CostUnknownCalls-unknownStart)
	if a.Trajectory != nil {
		// The store is bounded once per run, not per event: a directory listing
		// of at most a few hundred files is cheap, and bounding it here is what
		// keeps the record from growing without a limit nobody watches.
		if _, err := a.Trajectory.Prune(a.TrajectoryMaxRuns, a.TrajectoryMaxBytes); err != nil {
			tlog.Warn("trajectory", "prune", "dir", a.Trajectory.Dir(), "error", err)
		}
	}
}

// CurrentRun is the run in flight, or nil when nothing is being recorded. It is
// what a delegating tool hands to the agent it builds, so the child's file hangs
// off the parent's.
func (a *Agent) CurrentRun() *TrajectoryRun { return a.trajRun }

// splitProviderModel names the route a run was sent to. A provider that cannot
// name itself records neither field rather than a guess: an analysis that
// attributed a run to the wrong model would be worse than one that left it blank.
func splitProviderModel(a *Agent) (provider, model string) {
	key, ok := a.priceKey()
	if !ok {
		return "", ""
	}
	route, name, found := strings.Cut(key, "/")
	if !found {
		return "", key
	}
	return route, name
}

// recordFirstDelta wraps one call's callbacks so the delay to its first streamed
// delta is measured, chaining to whatever the caller already had.
//
// Both delta kinds count: a provider that thinks out loud emits reasoning first,
// and the user waiting for an answer does not care which channel broke the
// silence. The measurement starts at the instant the request was sent, which is
// the instant the call is priced at, so a trajectory line's ttft_ms and its cost
// are about the same moment.
func (a *Agent) recordFirstDelta(cb *types.StreamCallbacks) *types.StreamCallbacks {
	a.ttft, a.ttftSet = 0, false
	if cb == nil {
		return nil
	}
	wrapped := *cb
	note := func() {
		if a.ttftSet || a.callStart.IsZero() {
			return
		}
		a.ttft, a.ttftSet = a.now().Sub(a.callStart), true
	}
	text := wrapped.OnTextDelta
	wrapped.OnTextDelta = func(delta string) {
		note()
		if text != nil {
			text(delta)
		}
	}
	reasoning := wrapped.OnReasoningDelta
	wrapped.OnReasoningDelta = func(delta string) {
		note()
		if reasoning != nil {
			reasoning(delta)
		}
	}
	return &wrapped
}

// recordFailover attaches a route change to the run's file and forwards it to the
// caller.
//
// The provider wrapper decides to fail over and has no step number; the loop has
// the step but does not see the decision. This wrapper joins the two: it is
// installed per call, so the event carries the step that made it. Without a run
// being recorded the wrapper still forwards, which is what makes the TUI and the
// terminal see the same event either way.
func (a *Agent) recordFailover(cb *types.StreamCallbacks, step int) *types.StreamCallbacks {
	if cb == nil {
		return nil
	}
	wrapped := *cb
	inner := cb.OnFailover
	wrapped.OnFailover = func(from, to string, err error) {
		if a.trajRun != nil {
			a.trajRun.RouteFailover(step, from, to, failureKind(err))
		}
		if inner != nil {
			inner(from, to, err)
		}
	}
	return &wrapped
}

// toolStatus names what a tool call produced.
//
// It is classified from the result text, because the text is what the loop has:
// the dispatcher turns every failure into a string for the model to read, and
// there is no separate status to read instead. The marker prefixes are the ones
// those paths actually emit, so the classification tracks the vocabulary rather
// than an idea of one; a tool whose own output happens to begin with "error"
// will be recorded as a failure, which is the honest reading of a string the
// loop cannot distinguish from one it wrote.
func toolStatus(result string) string {
	trimmed := strings.TrimSpace(result)
	switch {
	case types.RefusalIsTerminal(result):
		return "blocked"
	case strings.HasPrefix(trimmed, types.RefusalMarker):
		return "refused"
	case strings.HasPrefix(trimmed, "[LOOP]"):
		return "repeated"
	case strings.HasPrefix(trimmed, "[DENIED]"):
		return "error"
	case strings.HasPrefix(trimmed, "error"):
		return "error"
	case strings.HasPrefix(trimmed, "invalid arguments for "):
		return "error"
	case strings.HasPrefix(trimmed, "unknown tool: "):
		return "error"
	}
	return "ok"
}

// costSince subtracts what the session had already spent, per unit, so a run's
// line reports the run rather than the session. A unit that did not move is
// omitted rather than recorded as zero.
func costSince(after, before types.CostTotals) types.CostTotals {
	if len(after) == 0 {
		return nil
	}
	out := make(types.CostTotals, len(after))
	for unit, amount := range after {
		if delta := amount - before[unit]; delta != 0 {
			out[unit] = delta
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
