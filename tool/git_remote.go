package tool

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// pushTimeout bounds a push. It is a network call, and the failure a hung
// credential prompt or an unreachable remote produces is worse than the error a
// deadline reports.
const pushTimeout = 60 * time.Second

// CurrentBranch returns the branch HEAD points at, or an error when HEAD is
// detached — a push needs a branch to push.
func CurrentBranch(ctx context.Context) (string, error) {
	branch, err := gitOutput(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	if branch == "HEAD" {
		return "", fmt.Errorf("HEAD is detached: check out a branch before pushing")
	}
	return branch, nil
}

// DefaultBranch reports the repository's default branch name.
//
// It trusts the remote's HEAD first (`origin/HEAD`, which a clone sets), falls
// back to the local branch when only one of `main`/`master` exists, and reports
// "" when it cannot tell. The caller must treat "" as "no branch is known to be
// default" rather than as "none is": guessing the wrong way would either block a
// legitimate push or allow one onto the default branch, and only the first is
// recoverable.
func DefaultBranch(ctx context.Context) string {
	if ref, err := gitOutput(ctx, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimPrefix(ref, "origin/")
	}
	var found []string
	for _, name := range []string{"main", "master"} {
		if _, err := gitOutput(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err == nil {
			found = append(found, name)
		}
	}
	if len(found) == 1 {
		return found[0]
	}
	return ""
}

// PushBranch pushes the current branch to origin and reports what happened.
//
// It refuses the default branch — a run's work belongs on a branch, and the
// repository's own rule is branch → PR → merge — and it uses
// `--force-with-lease` rather than `--force`, so a force is refused when the
// remote moved since the last fetch. Push and pull request are user commands,
// not model-callable tools: the model prepares the branch, the user spends the
// credentials (roadmap C2).
func PushBranch(ctx context.Context, force bool) (string, error) {
	branch, err := CurrentBranch(ctx)
	if err != nil {
		return "", err
	}
	if def := DefaultBranch(ctx); def != "" && branch == def {
		return "", fmt.Errorf("refusing to push the default branch %q: create a branch first", branch)
	}
	if _, err := gitOutput(ctx, "remote", "get-url", "origin"); err != nil {
		return "", fmt.Errorf("no origin remote to push to: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, pushTimeout)
	defer cancel()

	args := []string{"push", "-u", "origin", "HEAD"}
	if force {
		args = []string{"push", "--force-with-lease", "-u", "origin", "HEAD"}
	}
	out, err := gitOutput(ctx, args...)
	if err != nil {
		return "", pushError(branch, err)
	}
	return fmt.Sprintf("Pushed %s to origin (upstream set).\n%s", branch, out), nil
}

// pushError turns git's failure into one a person can act on, without hiding
// what git said: the classification is a hint in front of the original error.
func pushError(branch string, err error) error {
	text := err.Error()
	switch {
	case strings.Contains(text, "Authentication failed"),
		strings.Contains(text, "could not read Username"),
		strings.Contains(text, "Permission denied"),
		strings.Contains(text, "publickey"):
		return fmt.Errorf("push of %s failed at authentication (git said: %s)", branch, text)
	case strings.Contains(text, "non-fast-forward"),
		strings.Contains(text, "rejected"),
		strings.Contains(text, "fetch first"):
		return fmt.Errorf("push of %s was rejected: the remote has commits this branch does not (pull or rebase first; /push --force uses --force-with-lease)", branch)
	case strings.Contains(text, "no such remote"), strings.Contains(text, "does not appear to be a git repository"):
		return fmt.Errorf("push of %s failed: origin is not reachable (git said: %s)", branch, text)
	default:
		return fmt.Errorf("push of %s failed: %s", branch, text)
	}
}
