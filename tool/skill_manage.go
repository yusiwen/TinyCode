package tool

import (
	"context"
	"fmt"
	"strings"

	"github.com/yusiwen/tinycode/skill"
)

// SkillManage returns a Tool that allows the agent to create, edit, delete,
// and list skills. This enables self-improvement — the agent can author its
// own reusable capabilities without code changes.
func SkillManage() Tool {
	return Tool{
		Name:        "skill_manage",
		Description: "Manage skills: create, edit, delete, or list. " +
			"Skills are reusable instruction documents (SKILL.md format) that guide tool usage. " +
			"Use this to create new skills, update existing ones, remove outdated ones, or list all available skills.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"create", "edit", "delete", "list"},
					"description": "Operation to perform: create, edit, delete, or list skills",
				},
				"name": map[string]any{
					"type":        "string",
					"description": "Skill name (required for create/edit/delete)",
				},
				"content": map[string]any{
					"type":        "string",
					"description": "Full SKILL.md content including YAML frontmatter (required for create/edit). Must start with '---\\nname: <name>\\ndescription: <desc>\\n---'",
				},
			},
			"required": []string{"action"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			action, _ := args["action"].(string)
			name, _ := args["name"].(string)
			content, _ := args["content"].(string)

			switch action {
			case "list":
				all := skill.ListAll(".")
				if len(all) == 0 {
					return "No skills found.", nil
				}
				var b strings.Builder
				b.WriteString(fmt.Sprintf("Found %d skills:\n", len(all)))
				for _, s := range all {
					label := "🔵 builtin"
					if s.Source == "user" {
						label = "🟢 user"
					}
					b.WriteString(fmt.Sprintf("  %s  %s — %s\n", label, s.Skill.Name, s.Skill.Description))
				}
				return b.String(), nil

			case "create":
				if content == "" {
					return "", fmt.Errorf("content is required for create")
				}
				createdName, err := skill.CreateOne(content)
				if err != nil {
					return "", fmt.Errorf("create: %w", err)
				}
				return fmt.Sprintf("Created skill '%s' at ~/.tinycode/skills/%s/SKILL.md", createdName, createdName), nil

			case "edit":
				if name == "" {
					return "", fmt.Errorf("name is required for edit")
				}
				if content == "" {
					return "", fmt.Errorf("content is required for edit")
				}
				isOverride, err := skill.EditOne(name, content)
				if err != nil {
					return "", fmt.Errorf("edit: %w", err)
				}
				msg := fmt.Sprintf("Updated skill '%s'", name)
				if isOverride {
					msg += " (overrides builtin — saved to ~/.tinycode/skills/)"
				}
				return msg, nil

			case "delete":
				if name == "" {
					return "", fmt.Errorf("name is required for delete")
				}
				if err := skill.DeleteOne(name); err != nil {
					return "", fmt.Errorf("delete: %w", err)
				}
				return fmt.Sprintf("Deleted skill '%s'", name), nil

			default:
				return "", fmt.Errorf("unknown action: %s (use: create, edit, delete, list)", action)
			}
		},
	}
}
