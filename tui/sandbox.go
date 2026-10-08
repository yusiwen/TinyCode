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
	switch {
	case !tool.ConfineCommands():
		b.WriteString("  command confinement: off\n")
	case tool.CommandConfinementAvailable():
		b.WriteString("  command confinement: on (available)\n")
	default:
		b.WriteString("  command confinement: on, but UNAVAILABLE on this host — commands are refused\n")
	}

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
	return b.String()
}
