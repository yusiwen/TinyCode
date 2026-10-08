package types

import (
	"strings"
	"testing"
)

// TestRefusalShapeIsOneShape pins the vocabulary: every refusal carries the same
// marker and the same fields, and what differs is a field, not a prefix.
func TestRefusalShapeIsOneShape(t *testing.T) {
	asks := []string{AskInteractive, AskUnavailable, AskCapability, AskPolicy, AskMode}
	for _, ask := range asks {
		t.Run(ask, func(t *testing.T) {
			message := Refusal{
				Subject: `path "/etc/passwd"`,
				Reason:  "outside the project root",
				Mode:    SandboxWorkspaceWrite,
				Ask:     ask,
				Detail:  []string{"rule: the path fence"},
			}.Message()

			for _, want := range []string{
				RefusalMarker,
				`path "/etc/passwd"`,
				"outside the project root",
				"mode: " + string(SandboxWorkspaceWrite),
				"ask: " + ask,
				"rule: the path fence",
				"next: ",
			} {
				if !strings.Contains(message, want) {
					t.Errorf("refusal for %q is missing %q:\n%s", ask, want, message)
				}
			}
			if !strings.HasPrefix(message, RefusalMarker) {
				t.Errorf("the marker must begin the refusal:\n%s", message)
			}
		})
	}
}

// TestUnknownAskStillNamesAHint covers the drift direction that matters: a new
// ask value must not produce a refusal that advises nothing.
func TestUnknownAskStillNamesAHint(t *testing.T) {
	message := Refusal{Subject: "command", Reason: "refused", Ask: "something-new"}.Message()
	if !strings.Contains(message, "next: ") {
		t.Fatalf("a refusal with an unregistered ask value gave no guidance:\n%s", message)
	}
}

// TestRefusalAskReadsTheField pins what the agent loop keys off.
func TestRefusalAskReadsTheField(t *testing.T) {
	refusal := Refusal{Subject: "command", Reason: "refused", Ask: AskPolicy}.Message()
	if ask, ok := RefusalAsk(refusal); !ok || ask != AskPolicy {
		t.Fatalf("RefusalAsk = %q, %v; want %q, true", ask, ok, AskPolicy)
	}

	if _, ok := RefusalAsk("STDOUT:\nhello\n"); ok {
		t.Error("an ordinary result was read as a refusal")
	}
	if ask, ok := RefusalAsk(RefusalMarker + " something\n"); !ok || ask != "" {
		t.Errorf("a refusal without an ask field should still be one, got %q, %v", ask, ok)
	}

	// A command can print anything, including this marker. Output is not a
	// refusal unless it opens the result.
	printed := "STDOUT:\n" + RefusalMarker + " path \"/x\": y\n  ask: capability\nnext: whatever\n"
	if _, ok := RefusalAsk(printed); ok {
		t.Error("a marker inside command output was read as a refusal")
	}
	if RefusalIsTerminal(printed) {
		t.Error("a marker inside command output was treated as a terminal refusal")
	}
}

// TestRefusalIsTerminal pins which refusals stop the loop: only the ones nothing
// the model tries can change. A path it may not touch and a mode it can switch
// are not terminal — the loop hands those to the model with the hint attached.
func TestRefusalIsTerminal(t *testing.T) {
	cases := map[string]bool{
		AskCapability:  true,
		AskPolicy:      true,
		AskInteractive: false,
		AskUnavailable: false,
		AskMode:        false,
	}
	for ask, want := range cases {
		result := Refusal{Subject: "x", Reason: "y", Ask: ask}.Message()
		if got := RefusalIsTerminal(result); got != want {
			t.Errorf("RefusalIsTerminal(ask=%q) = %v, want %v", ask, got, want)
		}
	}
	if RefusalIsTerminal("STDOUT:\nall good\n") {
		t.Error("an ordinary result was treated as a terminal refusal")
	}
}
