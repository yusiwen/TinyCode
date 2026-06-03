package skill

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// NewGitCommitSkill creates a skill that shows staged changes for review
// without actually committing. The LLM must confirm before the commit is made.
func NewGitCommitSkill() Skill {
	return Skill{
		Name:        "git-commit",
		Description: "Show staged changes and generate a commit message for LLM confirmation.",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			var statBuf, statusBuf bytes.Buffer

			statCmd := exec.CommandContext(ctx, "git", "diff", "--stat", "--cached")
			statCmd.Stdout = &statBuf
			statCmd.Stderr = &statBuf
			if err := statCmd.Run(); err != nil {
				return "", fmt.Errorf("git diff --stat --cached: %w\n%s", err, statBuf.String())
			}

			statusCmd := exec.CommandContext(ctx, "git", "status", "--short")
			statusCmd.Stdout = &statusBuf
			statusCmd.Stderr = &statusBuf
			if err := statusCmd.Run(); err != nil {
				return "", fmt.Errorf("git status --short: %w\n%s", err, statusBuf.String())
			}

			msg, _ := args["message"].(string)

			result := fmt.Sprintf(
				"## Staged Changes (stat)\n\n%s\n\n## Working Tree Status\n\n%s\n\n## Proposed Message\n\n%s\n\n## Confirmation\n\nReply with `confirm` to proceed with the commit, or provide a new message to update it.",
				statBuf.String(),
				statusBuf.String(),
				msg,
			)
			return result, nil
		},
	}
}
