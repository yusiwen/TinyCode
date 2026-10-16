package tui

import (
	"fmt"
	"strings"

	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/types"
)

// The permission dialog's answers. "Always allow" is the only one that outlives
// the session, so its label names the file it writes: a person choosing it is
// changing a file they will otherwise never see, and the choice is only
// deliberate if that is visible when it is made.
const (
	allowOnceLabel    = "Allow once"
	allowSessionLabel = "Allow session"
	alwaysAllowPrefix = "Always allow"
)

func alwaysAllowLabel() string {
	path, err := config.UserConfigPath()
	if err != nil {
		return alwaysAllowPrefix + " (saves to your config)"
	}
	return alwaysAllowPrefix + " (saves to " + path + ")"
}

// sandboxReport renders what actually confines file operations in this process,
// for the /sandbox command.
//
// The facts come from the tool package, where they otherwise exist only as a
// log line. Printing them here is what makes a degraded host visible to the
// person running the agent — and what a scenario can assert, since a fact that
// lives only in a tool result is never drawn on screen.
func sandboxReport() string {
	info := tool.ContainmentInfo()

	required := "no"
	if tool.HardBoundaryRequired() {
		required = "yes"
	}

	root := tool.DefaultSandbox.ProjectRoot
	if root == "" {
		root = "(unset — file fencing is off)"
	}

	var b strings.Builder
	b.WriteString("Sandbox\n")
	fmt.Fprintf(&b, "  containment: %s\n", info.String())
	fmt.Fprintf(&b, "  hard boundary required: %s\n", required)

	// Command confinement is its own capability and its own switch: a host can
	// enforce the agent's opens and still have no way to confine a subprocess.
	// The line says why when it is off, so the per-platform default (off where
	// no mechanism exists) is stated on the host it applies to rather than
	// leaving commands silently unconfined.
	fmt.Fprintf(&b, "  command confinement: %s\n", tool.CommandConfinementStatus())

	fmt.Fprintf(&b, "  project root: %s\n", root)

	// The roots come from the same resolver a run uses, so the report cannot
	// describe a writable set the fence would not grant.
	roots := tool.PolicyFor(types.SandboxWorkspaceWrite).Roots
	if len(roots) == 0 {
		b.WriteString("  writable roots: none\n")
	} else {
		b.WriteString("  writable roots:\n")
		for _, r := range roots {
			b.WriteString("    " + r + "\n")
		}
	}

	// The cache roots are the part of the writable set nobody chose by hand, so
	// the report says what grants them and what grants more. Without this line the
	// only way to learn why a toolchain's cache is writable — or why `go mod
	// download` failed on a cold one — is to read the source (issue #166).
	b.WriteString("  toolchain caches: granted for the toolchains installed on this host (go, cargo, npm, pnpm), " +
		"and never a directory on PATH; grant another path in your own ~/.tinycode/config.json, " +
		"under \"sandbox.allowed_path_grants\"\n")

	// What can be undone is part of the safety posture this command reports, and
	// the line that matters is the one naming what is *not* captured: a user who
	// reads "checkpoints" and assumes their bash edits are recoverable has been
	// misled by omission (issue #234).
	b.WriteString("  " + tool.CheckpointReport())

	return b.String()
}
