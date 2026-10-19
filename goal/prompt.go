package goal

import (
	"fmt"
	"strconv"
)

// RoundPrompt is the text one automatic round starts with.
//
// The objective is JSON-quoted, so a newline or a closing tag inside it cannot
// break the block. The rest says what the round may trust — the workspace and its
// own tool results, not a memory of earlier rounds — and how the goal ends: with
// evidence for a completion, and with a concrete, persistent condition for a
// blocker. The authority rules of the goal tool are stated where the model needs
// them: a round can finish or block the goal, not rewrite it.
func RoundPrompt(g *Goal, round int) string {
	if g == nil {
		return ""
	}
	// strconv.Quote, not JSON: both escape a newline, and this one leaves < and >
	// alone, so the objective stays readable without breaking the block.
	objective := strconv.Quote(g.Objective)
	return fmt.Sprintf(`<goal_round>
Objective: %s
Round: %d/%d

Continue working toward the objective.
Treat the workspace, the results of your own tool calls, and durable state (files, the session) as the authority: do not assume earlier work happened unless you can verify it.
Before you call the goal complete, produce evidence — a command, a test, or a file you can point at — and describe it in your reply.
If the same concrete condition keeps blocking progress and you can name it, call the goal tool with action blocked and that reason. Do not block on difficulty, uncertainty, or remaining work.
Use the goal tool (action complete) only when the objective is achieved; the goal's phase is the only signal that stops further rounds.
</goal_round>`, objective, round, g.MaxGoalRounds)
}
