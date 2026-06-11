package tool

import (
	"context"

	"github.com/yusiwen/tinycode/skill"
)

// LoadSkill returns a Tool that loads a skill's instructions by name.
// LLM calls it when it sees a skill in the Available skills list and
// wants to follow its instructions. Dedup: the same skill is only
// loaded once per session.
func LoadSkill() Tool {
	return Tool{
		Name:        "load_skill",
		Description: "Load a skill's instructions by name. The skill content (a markdown document) will be injected as the tool result. Use this when you see a skill in the 'Available skills' list that matches the user's request. Call this tool once per skill — repeated calls for the same skill are ignored.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "The name of the skill to load (e.g., code-review, git-commit)",
				},
			},
			"required": []string{"name"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			if name == "" {
				return "", nil
			}
			content, fresh := skill.LoadOnce(name, ".")
			if !fresh {
				return "", nil
			}
			return content, nil
		},
	}
}
