package tool

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func gitExec(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func GitStatus() Tool {
	return Tool{
		Name:        "git_status",
		Description: "Show the working tree status. Returns changed files, staged changes, and untracked files.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			return gitExec(ctx, "status")
		},
	}
}

func GitDiff() Tool {
	return Tool{
		Name:        "git_diff",
		Description: "Show changes. Returns unstaged diffs by default. Use 'staged' to show staged changes, or 'branch_a:branch_b' to compare branches.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"target": map[string]any{
					"type":        "string",
					"description": "Empty for unstaged, 'staged' for staged, or 'branch_name' to diff against a branch",
				},
			},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			target, _ := args["target"].(string)
			switch target {
			case "staged":
				return gitExec(ctx, "diff", "--cached")
			case "":
				return gitExec(ctx, "diff")
			default:
				return gitExec(ctx, "diff", target)
			}
		},
	}
}

func GitCommit() Tool {
	return Tool{
		Name:        "git_commit",
		Description: "Stage all changes and create a commit with the given message.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{
					"type":        "string",
					"description": "Commit message",
				},
			},
			"required": []string{"message"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			msg, _ := args["message"].(string)
			if msg == "" {
				return "", fmt.Errorf("message is required")
			}
			if _, err := gitExec(ctx, "add", "-A"); err != nil {
				return "", err
			}
			out, err := gitExec(ctx, "commit", "-m", msg)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Committed:\n%s", out), nil
		},
	}
}

func GitBranch() Tool {
	return Tool{
		Name:        "git_branch",
		Description: "List branches or switch to a branch. Shows current branch with '*'. Use --create to create a new branch.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Branch name to switch to, '--list' to list all (default), or '--create <name>' to create and switch",
				},
			},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			name, _ := args["name"].(string)
			switch {
			case name == "" || name == "--list":
				return gitExec(ctx, "branch")
			case strings.HasPrefix(name, "--create "):
				branch := strings.TrimPrefix(name, "--create ")
				return gitExec(ctx, "checkout", "-b", branch)
			default:
				return gitExec(ctx, "checkout", name)
			}
		},
	}
}

func GitLog() Tool {
	return Tool{
		Name:        "git_log",
		Description: "Show commit history. Default shows last 10 commits in one-line format.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{
					"type":        "number",
					"description": "Number of commits to show (default: 10)",
				},
			},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			limit := 10
			if l, ok := args["limit"].(float64); ok && l > 0 {
				limit = int(l)
			}
			format := "--pretty=format:%h %s (%ai, %an)"
			return gitExec(ctx, "log", fmt.Sprintf("-%d", limit), format)
		},
	}
}
