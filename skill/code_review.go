package skill

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/yusiwen/tinycode/tool"
)

// NewCodeReviewSkill creates a skill that reviews the diff of the last commit.
func NewCodeReviewSkill() Skill {
	return Skill{
		Name:        "code-review",
		Description: "Review the diff of the last commit and provide a summary of changes. " +
			"Output is truncated at 2000 lines or 50 KB; use read_file with offset/limit to view the full saved output.",
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			var statBuf, diffBuf bytes.Buffer

			statCmd := exec.CommandContext(ctx, "git", "diff", "--stat", "HEAD~1")
			statCmd.Stdout = &statBuf
			statCmd.Stderr = &statBuf
			if err := statCmd.Run(); err != nil {
				return "", fmt.Errorf("git diff --stat: %w\n%s", err, statBuf.String())
			}

			diffCmd := exec.CommandContext(ctx, "git", "diff", "HEAD~1")
			diffCmd.Stdout = &diffBuf
			diffCmd.Stderr = &diffBuf
			if err := diffCmd.Run(); err != nil {
				return "", fmt.Errorf("git diff: %w\n%s", err, diffBuf.String())
			}

			diffOutput := diffBuf.String()
			statOutput := statBuf.String()

			// Truncate the diff portion if it's too large
			trunc := tool.TruncateOutput(diffOutput)

			result := fmt.Sprintf(
				"## Changes (stat)\n\n%s\n\n## Full Diff\n\n```diff\n%s\n```",
				statOutput, trunc.Content,
			)
			return result, nil
		},
	}
}
