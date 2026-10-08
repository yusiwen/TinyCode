package types

import (
	"fmt"
	"strings"
)

// The refusal vocabulary.
//
// Every path that refuses an operation emits the same marker and the same
// shape. What differs — which path or command, why, and what the reader can do
// next — lives in fields, never in a new prefix, so a caller can act on the
// marker instead of on English that drifts between call sites.

// RefusalMarker begins every refusal. One marker, one shape.
const RefusalMarker = "[SANDBOX]"

// The `ask` field says what kind of recovery is possible, which is also what
// tells the agent loop whether to hand the refusal to the model or to stop.
const (
	// AskInteractive: a person can grant this, narrowly at first.
	AskInteractive = "interactive"
	// AskUnavailable: nobody can be asked right now, but the work can be done
	// differently (inside the roots).
	AskUnavailable = "unavailable"
	// AskCapability: the machine cannot do this at all. No approval creates a
	// kernel boundary, a missing server, or a removed rule.
	AskCapability = "capability"
	// AskPolicy: a configured rule forbids it. Configuration, not permission.
	AskPolicy = "policy"
	// AskMode: another mode would allow it.
	AskMode = "mode"
)

// refusalHints is the one hint per ask value. Naming the narrowest option first
// is the point: "allow once" costs the least and is what a reader should reach
// for before widening anything.
var refusalHints = map[string]string{
	AskInteractive: "choose the narrowest permission that works — allow once, then allow this session, then always allow, which writes the config file",
	AskUnavailable: "do not retry this path; work inside the writable roots, or ask the user to change the sandbox configuration or run interactively",
	AskCapability:  "do not retry; this is a capability of the machine, not a permission that can be granted",
	AskPolicy:      "do not retry this command; the rule is configuration (sandbox.deny_commands), not something the dialog or the model can grant",
	AskMode:        "switch to build mode (/build) to run commands that modify files",
}

// Refusal is one refused operation, in the shape every refusal path emits.
type Refusal struct {
	// Subject names what was refused: `path "/etc/passwd"`, `command "rm -rf /"`.
	Subject string
	// Reason says why, in one clause.
	Reason string
	// Mode is the run's mode when the refusal happened, so the reader can tell
	// a read-only boundary from a missing capability.
	Mode SandboxMode
	// Ask is one of the Ask* values and selects the hint.
	Ask string
	// Detail carries the specifics — the rule that matched, the launcher's own
	// diagnostic — one line each.
	Detail []string
}

// Message renders the refusal: the marker line, the fields, and the one hint.
func (r Refusal) Message() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %s\n", RefusalMarker, r.Subject, r.Reason)
	if r.Mode != "" {
		fmt.Fprintf(&b, "  mode: %s\n", r.Mode)
	}
	if r.Ask != "" {
		fmt.Fprintf(&b, "  ask: %s\n", r.Ask)
	}
	for _, line := range r.Detail {
		if line != "" {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	hint := refusalHints[r.Ask]
	if hint == "" {
		hint = "no recovery is available for this refusal"
	}
	fmt.Fprintf(&b, "next: %s", hint)
	return b.String()
}

// RefusalAsk reads the ask field out of a tool result that carries the marker.
// The second result reports whether the result is a refusal at all.
//
// The marker must open the result: a command that prints words resembling a
// refusal (a file it printed, a log it echoed) is output, not a refusal from
// the sandbox, and reading those as one would change how the loop treats the
// run.
func RefusalAsk(result string) (string, bool) {
	trimmed := strings.TrimLeft(result, " \t\r\n")
	if !strings.HasPrefix(trimmed, RefusalMarker) {
		return "", false
	}
	for _, line := range strings.Split(trimmed, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ask:"); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", true
}

// RefusalIsTerminal reports whether a refusal is one the agent loop should stop
// on rather than hand back to the model: nothing the model tries changes a
// capability, a missing answerer, or a configured rule. A refusal the model can
// adapt to — a path it may not touch, a mode it can switch — is not terminal,
// and the loop passes it on with the hint attached.
func RefusalIsTerminal(result string) bool {
	ask, ok := RefusalAsk(result)
	if !ok {
		return false
	}
	switch ask {
	case AskCapability, AskPolicy:
		return true
	default:
		return false
	}
}
