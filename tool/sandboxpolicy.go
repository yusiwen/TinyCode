package tool

import (
	"context"

	"github.com/yusiwen/tinycode/types"
)

// This file owns the one place a run's file-effect policy is derived from the
// sandbox configuration. Consumers read the policy off the run's context; none
// of them re-derives a mode or a root, so the path fence, the command boundary
// and the plan-mode guard cannot disagree about what is writable.

// PolicyFor builds the policy for a run starting in mode, from the
// configuration this package owns. It is called when a run starts, and the
// result is frozen onto that run's context.
func PolicyFor(mode types.SandboxMode) types.SandboxPolicy {
	return PolicyFromConfig(DefaultSandbox, mode)
}

// PolicyFromConfig is the one place a policy is derived from a sandbox
// configuration. Everything that needs a mode or a root goes through it —
// including a direct CheckPath on a configuration that never went through a run
// — so no consumer can invent a second answer.
func PolicyFromConfig(sc *SandboxConfig, mode types.SandboxMode) types.SandboxPolicy {
	return types.SandboxPolicy{
		Mode:        mode,
		ProjectRoot: sc.projectRootResolved(),
		Roots:       sc.WritableRoots(),
		Source:      "sandbox configuration",
	}
}

// runPolicy returns the policy governing one call: the run's frozen policy when
// its context carries one, and a policy built from the live configuration
// otherwise.
//
// The fallback covers a direct library use that never went through the agent's
// run setup. It is workspace-write rather than read-only on purpose: guessing a
// narrower boundary would silently break a caller's commands, and guessing a
// wider one is what a boundary is for.
func runPolicy(ctx context.Context) types.SandboxPolicy {
	if policy, ok := types.SandboxPolicyFrom(ctx); ok {
		return policy
	}
	return PolicyFor(types.SandboxWorkspaceWrite)
}

// InstallSandboxPolicyResolver wires this package's configuration into the run
// builder in the agent package, which cannot import it. Called once during
// composition; without it a run gets a policy with no roots, which is the
// honest answer when nothing has configured a sandbox.
func InstallSandboxPolicyResolver() {
	types.ResolveSandboxRoots = func(mode types.SandboxMode) (string, []string) {
		policy := PolicyFor(mode)
		return policy.ProjectRoot, policy.Roots
	}
}
