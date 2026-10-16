package tui

import (
	"strings"
	"testing"
)

// TestSandboxReportStatesTheCachePolicy is the "state it where a user meets it"
// half of issue #166: a grant nobody chose by hand has to be explained where it
// is visible, or the only way to learn why a toolchain's cache is writable — or
// why a cold `go mod download` failed — is to read the source.
func TestSandboxReportStatesTheCachePolicy(t *testing.T) {
	report := sandboxReport()

	for _, want := range []string{
		"writable roots:",             // the set itself
		"toolchain caches:",           // what grants the caches inside it
		"sandbox.allowed_path_grants", // the key that grants one more
		"~/.tinycode/config.json",     // and the file a project may not extend it from
		"checkpoints:",                // what can be undone, and where it lives (issue #234)
		"not captured:",               // and the half a user would otherwise assume
	} {
		if !strings.Contains(report, want) {
			t.Errorf("the /sandbox report does not mention %q:\n%s", want, report)
		}
	}
}
