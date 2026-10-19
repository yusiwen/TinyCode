package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/goal"
	"github.com/yusiwen/tinycode/types"
)

type goalPayload struct {
	Goal  *goal.Goal `json:"goal"`
	Armed bool       `json:"armed"`
}

func decodeGoal(t *testing.T, out string) goalPayload {
	t.Helper()
	var payload goalPayload
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, out)
	}
	return payload
}

// goalTestTool returns the tool over a fresh service.
func goalTestTool() Tool {
	return Goal(goal.NewService())
}

// goalRoundCtx is an automatic round's context: the source the goal tool treats
// as the one that may not rewrite the objective.
func goalRoundCtx() context.Context {
	return types.WithGoalRound(context.Background(), types.GoalRunInfo{
		GoalID: "goal-1", Round: 1, Source: types.GoalSourceDriver,
	})
}

func execute(t *testing.T, tl Tool, ctx context.Context, args map[string]any) (string, error) {
	t.Helper()
	return tl.Execute(ctx, args)
}

func TestGoalToolSchemaIsValidated(t *testing.T) {
	params := goalSchema()
	required, _ := params["required"].([]string)
	if len(required) != 1 || required[0] != "action" {
		t.Errorf("required = %v, want [action]", required)
	}
	props, _ := params["properties"].(map[string]any)
	action, _ := props["action"].(map[string]any)
	enum, _ := action["enum"].([]string)
	want := []string{"get", "create", "edit", "pause", "resume", "complete", "blocked"}
	if strings.Join(enum, ",") != strings.Join(want, ",") {
		t.Errorf("action enum = %v, want %v", enum, want)
	}
}

func TestGoalToolGetWithoutAGoal(t *testing.T) {
	out, err := execute(t, goalTestTool(), context.Background(), map[string]any{"action": "get"})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	payload := decodeGoal(t, out)
	if payload.Goal != nil {
		t.Errorf("goal = %+v, want null", payload.Goal)
	}
	if payload.Armed {
		t.Error("armed must be false with no goal")
	}
}

func TestGoalToolCreateFromAHumanTurn(t *testing.T) {
	tl := goalTestTool()
	out, err := execute(t, tl, context.Background(), map[string]any{
		"action": "create", "objective": "make the failing test pass",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	payload := decodeGoal(t, out)
	if payload.Goal == nil || payload.Goal.Objective != "make the failing test pass" {
		t.Fatalf("created goal = %+v", payload.Goal)
	}
	if !payload.Armed {
		t.Error("a created goal must be armed")
	}
	if payload.Goal.MaxGoalRounds != goal.DefaultMaxRounds {
		t.Errorf("max rounds = %d, want the default", payload.Goal.MaxGoalRounds)
	}

	// A second create is refused while the first is unfinished.
	if _, err := execute(t, tl, context.Background(), map[string]any{"action": "create", "objective": "another"}); err == nil {
		t.Error("a second goal must be refused while the first is unfinished")
	}
}

func TestGoalToolCreateNeedsAHumanTurn(t *testing.T) {
	ctx := goalRoundCtx()
	_, err := execute(t, goalTestTool(), ctx, map[string]any{"action": "create", "objective": "sneak one in"})
	if err == nil {
		t.Fatal("a goal round created a goal")
	}
	if !strings.Contains(err.Error(), "direct human request") {
		t.Errorf("error = %v, want it to name the authority rule", err)
	}
}

func TestGoalToolRefusesASubagent(t *testing.T) {
	ctx := types.WithSubagent(context.Background())
	for _, action := range []string{"create", "edit", "pause", "resume", "complete", "blocked"} {
		if _, err := execute(t, goalTestTool(), ctx, map[string]any{
			"action": action, "objective": "x", "goal_id": "g", "revision": 1, "reason": "y",
		}); err == nil {
			t.Errorf("%s was allowed in a sub-agent", action)
		} else if !strings.Contains(err.Error(), "sub-agent") {
			t.Errorf("%s: error = %v, want it to name the sub-agent rule", action, err)
		}
	}
	// Reading is still allowed: a child may need to know what it is working for.
	if _, err := execute(t, goalTestTool(), ctx, map[string]any{"action": "get"}); err != nil {
		t.Errorf("get was refused in a sub-agent: %v", err)
	}
}

func TestGoalToolMutationsNeedARefAndTheCurrentRevision(t *testing.T) {
	tl := goalTestTool()
	out, err := execute(t, tl, context.Background(), map[string]any{"action": "create", "objective": "the objective"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	created := decodeGoal(t, out).Goal

	if _, err := execute(t, tl, context.Background(), map[string]any{"action": "edit", "objective": "new"}); err == nil {
		t.Error("an edit without a ref was allowed")
	} else if !strings.Contains(err.Error(), "call get first") {
		t.Errorf("error = %v, want the read-first instruction", err)
	}

	// A stale revision is refused: the model must read again.
	stale := map[string]any{"action": "edit", "objective": "new", "goal_id": created.ID, "revision": created.Revision + 1}
	if _, err := execute(t, tl, context.Background(), stale); err == nil {
		t.Error("a stale revision was accepted")
	}

	current := map[string]any{"action": "edit", "objective": "new", "goal_id": created.ID, "revision": created.Revision}
	out, err = execute(t, tl, context.Background(), current)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got := decodeGoal(t, out).Goal; got.Objective != "new" || got.Revision != created.Revision+1 {
		t.Errorf("edited goal = %+v", got)
	}
}

func TestGoalToolPauseResumeStayHuman(t *testing.T) {
	tl := goalTestTool()
	out, err := execute(t, tl, context.Background(), map[string]any{"action": "create", "objective": "the objective"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	created := decodeGoal(t, out).Goal
	round := goalRoundCtx()

	for _, action := range []string{"pause", "resume", "edit"} {
		if _, err := execute(t, tl, round, map[string]any{
			"action": action, "objective": "x", "goal_id": created.ID, "revision": created.Revision,
		}); err == nil {
			t.Errorf("%s was allowed in a goal round", action)
		}
	}
}

// TestGoalToolCompleteFromAGoalRound is the other half of the authority rule: a
// round finishes the work it was given.
func TestGoalToolCompleteFromAGoalRound(t *testing.T) {
	tl := goalTestTool()
	out, err := execute(t, tl, context.Background(), map[string]any{"action": "create", "objective": "the objective"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	created := decodeGoal(t, out).Goal

	out, err = execute(t, tl, goalRoundCtx(), map[string]any{
		"action": "complete", "goal_id": created.ID, "revision": created.Revision,
	})
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	payload := decodeGoal(t, out)
	if payload.Goal.Phase != goal.PhaseComplete {
		t.Errorf("phase = %s, want complete", payload.Goal.Phase)
	}
	if payload.Armed {
		t.Error("completing must disarm the goal")
	}
}

func TestGoalToolBlockedFromAGoalRound(t *testing.T) {
	tl := goalTestTool()
	out, err := execute(t, tl, context.Background(), map[string]any{"action": "create", "objective": "the objective"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	created := decodeGoal(t, out).Goal
	round := goalRoundCtx()

	if _, err := execute(t, tl, round, map[string]any{
		"action": "blocked", "goal_id": created.ID, "revision": created.Revision,
	}); err == nil {
		t.Fatal("blocked without a reason was allowed")
	}

	out, err = execute(t, tl, round, map[string]any{
		"action": "blocked", "goal_id": created.ID, "revision": created.Revision,
		"reason": "the repository has no go.sum and the proxy is unreachable",
	})
	if err != nil {
		t.Fatalf("blocked: %v", err)
	}
	payload := decodeGoal(t, out)
	if payload.Goal.Phase != goal.PhaseBlocked {
		t.Fatalf("phase = %s, want blocked", payload.Goal.Phase)
	}
	if payload.Goal.Blocker == nil || payload.Goal.Blocker.Code != goal.BlockerModelReported {
		t.Errorf("blocker = %+v, want the model-reported code", payload.Goal.Blocker)
	}
	if !strings.Contains(payload.Goal.Blocker.Reason, "no go.sum") {
		t.Errorf("blocker reason = %q", payload.Goal.Blocker.Reason)
	}
}

func TestGoalToolUnknownAction(t *testing.T) {
	if _, err := execute(t, goalTestTool(), context.Background(), map[string]any{"action": "delete"}); err == nil {
		t.Error("an unknown action was accepted")
	}
	if _, err := execute(t, Goal(nil), context.Background(), map[string]any{"action": "get"}); err == nil {
		t.Error("a nil service was accepted")
	}
}

// TestGoalToolHumanAttributionMayCreate: a run attributed to the goal but started
// by a human keeps human authority, so "continue" after a resume can re-create or
// edit the goal.
func TestGoalToolHumanAttributionMayCreate(t *testing.T) {
	ctx := types.WithGoalRound(context.Background(), types.GoalRunInfo{
		GoalID: "goal-1", Round: 2, Source: types.GoalSourceHuman,
	})
	if _, err := execute(t, goalTestTool(), ctx, map[string]any{
		"action": "create", "objective": "a human asked for this",
	}); err != nil {
		t.Fatalf("a human-attributed run could not create a goal: %v", err)
	}
}
