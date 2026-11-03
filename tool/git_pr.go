package tool

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// prTimeout bounds the external call. Creating a pull request is a network call
// with an authentication step, and both can hang.
const prTimeout = 60 * time.Second

// gitLookPath is exec.LookPath behind a variable so a test can say "gh is not
// installed" without touching the machine's PATH.
var gitLookPath = exec.LookPath

// PullRequestOptions is what the user decided in the confirmation dialog.
type PullRequestOptions struct {
	Title string
	Body  string
}

// OpenPullRequest opens a pull request for the current branch.
//
// With `gh` (GitHub CLI) installed it runs `gh pr create`; without it, it runs
// nothing and returns the compare URL a person opens by hand — the same
// information, minus an external dependency. It refuses the default branch and a
// branch with commits the remote does not have, because both produce a pull
// request that is wrong or empty (roadmap C2).
func OpenPullRequest(ctx context.Context, opts PullRequestOptions) (string, error) {
	if strings.TrimSpace(opts.Title) == "" {
		return "", fmt.Errorf("a pull request needs a title")
	}
	branch, err := CurrentBranch(ctx)
	if err != nil {
		return "", err
	}
	def := DefaultBranch(ctx)
	if def != "" && branch == def {
		return "", fmt.Errorf("refusing to open a pull request from the default branch %q: create a branch first", branch)
	}
	if _, err := gitOutput(ctx, "rev-parse", "--abbrev-ref", "@{u}"); err != nil {
		return "", fmt.Errorf("branch %q has no upstream: push it first (/push)", branch)
	}
	ahead, err := gitOutput(ctx, "rev-list", "--count", "@{u}..HEAD")
	if err == nil && strings.TrimSpace(ahead) != "0" {
		return "", fmt.Errorf("branch %q has %s commit(s) the remote does not have: push first (/push)", branch, strings.TrimSpace(ahead))
	}

	base := def
	if base == "" {
		base = "main"
	}
	ownerRepo, err := githubOwnerRepo(ctx)
	if err != nil {
		return "", err
	}
	if ownerRepo == "" {
		return "", fmt.Errorf("origin is not a GitHub remote: push succeeded, open the pull request on the host")
	}

	body := strings.TrimSpace(opts.Body)
	if body == "" {
		body = "Created with TinyCode."
		if subjects, err := gitOutput(ctx, "log", "--format=- %s", "-10"); err == nil && strings.TrimSpace(subjects) != "" {
			body += "\n\n" + subjects
		}
	}

	compare := fmt.Sprintf("https://github.com/%s/compare/%s...%s?expand=1", ownerRepo, base, branch)
	gh, lookErr := gitLookPath("gh")
	if lookErr != nil {
		return fmt.Sprintf("gh is not installed, so nothing was run. Open the pull request here:\n%s", compare), nil
	}

	ctx, cancel := context.WithTimeout(ctx, prTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gh, "pr", "create",
		"--base", base, "--head", branch, "--title", opts.Title, "--body", body)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr create failed: %v\n%s", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// githubOwnerRepo reads origin and returns "owner/repo" for a github.com remote,
// or "" for any other host.
func githubOwnerRepo(ctx context.Context) (string, error) {
	url, err := gitOutput(ctx, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("no origin remote: %w", err)
	}
	url = strings.TrimSuffix(strings.TrimSpace(url), ".git")
	switch {
	case strings.HasPrefix(url, "git@github.com:"):
		return strings.TrimPrefix(url, "git@github.com:"), nil
	case strings.HasPrefix(url, "https://github.com/"):
		return strings.TrimPrefix(url, "https://github.com/"), nil
	}
	return "", nil
}
