package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yusiwen/tinycode/goal"
	"github.com/yusiwen/tinycode/types"
)

// Goal returns the model-facing tool over a session's completion objective.
//
// The service owns every rule — which phase may move where, the round cap, the
// revision fence. This tool is the authority boundary and the wire shape: a
// direct human turn may create, edit, pause or resume; a goal round may read,
// complete or block; a sub-agent may do neither (types.GoalRound / Subagent).
func Goal(service *goal.Service) Tool {
	return Tool{
		Name: "goal",
		Description: "Inspect or update the session's completion objective. " +
			"Call get first and pass back the exact goal_id and revision for every mutation. " +
			"create, edit, pause and resume require a direct human request; complete (say what evidence proves it) " +
			"and blocked (name the concrete condition) may also be called during a goal round. " +
			"One goal per session: an unfinished goal must be cleared, completed or blocked before another is created. " +
			"Returns JSON {goal, armed}, with goal null when none is current.",
		Parameters: goalSchema(),
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			return executeGoal(ctx, service, args)
		},
	}
}

func goalSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"get", "create", "edit", "pause", "resume", "complete", "blocked"},
				"description": "get reads the goal; create starts one; edit replaces the objective; pause and resume stop and restart continuation; complete marks it achieved; blocked records why it cannot proceed.",
			},
			"objective": map[string]any{
				"type":        "string",
				"description": "The objective, for create and edit.",
			},
			"max_goal_rounds": map[string]any{
				"type":        "integer",
				"description": "Optional round cap for create. Defaults to the session's configured cap (256).",
			},
			"goal_id": map[string]any{
				"type":        "string",
				"description": "The id get returned. Required for every mutation.",
			},
			"revision": map[string]any{
				"type":        "integer",
				"description": "The revision get returned. Required for every mutation; a stale value is refused rather than overwriting newer state.",
			},
			"reason": map[string]any{
				"type":        "string",
				"description": "For blocked: the concrete condition that persisted and prevents progress.",
			},
		},
		"required": []string{"action"},
	}
}

func executeGoal(ctx context.Context, service *goal.Service, args map[string]any) (string, error) {
	if service == nil {
		return "", fmt.Errorf("no goal service is attached")
	}
	action, _ := args["action"].(string)
	if action == "" {
		return "", fmt.Errorf("goal: action is required")
	}
	if err := goalAuthority(ctx, action); err != nil {
		return "", err
	}

	switch action {
	case "get":
		return goalJSON(service), nil
	case "create":
		objective, _ := args["objective"].(string)
		maxRounds := 0
		if raw, ok := args["max_goal_rounds"]; ok {
			maxRounds = intValue(raw)
		}
		if _, err := service.Create(objective, maxRounds); err != nil {
			return "", err
		}
		return goalJSON(service), nil
	case "edit":
		objective, _ := args["objective"].(string)
		ref, err := goalRef(args)
		if err != nil {
			return "", err
		}
		if _, err := service.Edit(ref, objective); err != nil {
			return "", err
		}
		return goalJSON(service), nil
	case "pause":
		ref, err := goalRef(args)
		if err != nil {
			return "", err
		}
		if _, err := service.Pause(ref); err != nil {
			return "", err
		}
		return goalJSON(service), nil
	case "resume":
		ref, err := goalRef(args)
		if err != nil {
			return "", err
		}
		if _, err := service.Resume(ref); err != nil {
			return "", err
		}
		return goalJSON(service), nil
	case "complete":
		ref, err := goalRef(args)
		if err != nil {
			return "", err
		}
		if _, err := service.Complete(ref); err != nil {
			return "", err
		}
		return goalJSON(service), nil
	case "blocked":
		ref, err := goalRef(args)
		if err != nil {
			return "", err
		}
		reason, _ := args["reason"].(string)
		if strings.TrimSpace(reason) == "" {
			return "", fmt.Errorf("blocked needs a reason: name the concrete condition that prevents progress")
		}
		if _, err := service.Block(ref, goal.BlockerModelReported, reason); err != nil {
			return "", err
		}
		return goalJSON(service), nil
	default:
		return "", fmt.Errorf("goal: unknown action %q", action)
	}
}

// goalAuthority refuses an action the current turn may not take. It runs before
// the service, so a refusal never mutates anything.
func goalAuthority(ctx context.Context, action string) error {
	// Reading is always allowed, including in a sub-agent: a child that knows
	// what the session is trying to achieve does better work, and a read changes
	// nothing.
	if action == "get" {
		return nil
	}
	if types.InSubagent(ctx) {
		return fmt.Errorf("goal: a sub-agent cannot manage the session goal; report back and let the parent decide")
	}
	switch action {
	case "create", "edit", "pause", "resume":
		if types.InGoalRound(ctx) {
			return fmt.Errorf("goal: %s needs a direct human request; a goal round may only read, complete or block the goal", action)
		}
	}
	return nil
}

// goalRef reads the {id, revision} a mutation must echo.
func goalRef(args map[string]any) (goal.Ref, error) {
	id, _ := args["goal_id"].(string)
	revision := intValue(args["revision"])
	if strings.TrimSpace(id) == "" || revision <= 0 {
		return goal.Ref{}, fmt.Errorf("goal: goal_id and revision are required; call get first")
	}
	return goal.Ref{ID: id, Revision: revision}, nil
}

// intValue accepts the shapes a JSON number can arrive in: the dispatcher
// unmarshals arguments into map[string]any, where numbers are float64.
func intValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case float32:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return 0
}

// goalJSON is the compact view the model sees after every call.
func goalJSON(service *goal.Service) string {
	payload := struct {
		Goal  *goal.Goal `json:"goal"`
		Armed bool       `json:"armed"`
	}{Goal: service.Snapshot(), Armed: service.Armed()}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf(`{"goal":null,"error":%q}`, err.Error())
	}
	return string(data)
}
