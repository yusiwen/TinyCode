package agent

import (
	"strings"
	"testing"
)

// buildPrompt returns the build agent's system prompt.
func buildPrompt(t *testing.T) string {
	t.Helper()
	cfg, ok := DefaultAgents()["build"]
	if !ok {
		t.Fatal("no build agent")
	}
	return cfg.SystemPrompt
}

// TestBuildPromptTellsTheModelWhenToCreateAGoal is the only part of the
// "automatic" goal creation a test can assert: the prompt the model reads. The
// behaviour itself is the model's judgement and is not deterministic, which is
// why a stub-backed scenario would prove the script rather than the decision.
func TestBuildPromptTellsTheModelWhenToCreateAGoal(t *testing.T) {
	prompt := buildPrompt(t)
	for _, want := range []string{
		"goal tool (action create)",
		"will not fit in one turn",
		"one goal per session",
		"goal get",
		"goal_id and revision",
		"action resume",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the build prompt does not mention %q", want)
		}
	}
}

// TestPlanPromptDoesNotOfferGoalCreation: plan mode's ruleset denies the goal
// tool, so telling the model to create a goal there would be an instruction it
// cannot follow.
func TestPlanPromptDoesNotOfferGoalCreation(t *testing.T) {
	cfg, ok := DefaultAgents()["plan"]
	if !ok {
		t.Fatal("no plan agent")
	}
	if strings.Contains(cfg.SystemPrompt, "goal tool") {
		t.Error("the plan prompt tells the model to use a tool its ruleset denies")
	}
	if cfg.IsToolAllowed("goal") {
		t.Error("plan mode allows the goal tool; the prompt test's premise is wrong")
	}
}
