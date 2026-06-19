package agent

import (
	"testing"
)

func TestWildcardMatchExact(t *testing.T) {
	if !wildcardMatch("bash", "bash") {
		t.Fatal("expected exact match")
	}
	if wildcardMatch("bash", "write_file") {
		t.Fatal("expected no match")
	}
}

func TestWildcardStar(t *testing.T) {
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"*", "anything", true},
		{"bash:*", "bash:ls -la", true},
		{"bash:*", "write_file:foo", false},
		{"bash:ls *", "bash:ls -la", true},
		{"bash:ls *", "bash:find .", false},
		{"bash:mkdir *", "bash:mkdir /tmp/x", true},
		{"edit:*", "edit:foo.txt", true},
		{"edit:*", "read:foo.txt", false},
	}
	for _, c := range cases {
		got := wildcardMatch(c.pattern, c.value)
		if got != c.want {
			t.Errorf("wildcardMatch(%q, %q) = %v, want %v", c.pattern, c.value, got, c.want)
		}
	}
}

func TestWildcardQuestion(t *testing.T) {
	if !wildcardMatch("mvn?", "mvna") {
		t.Fatal("expected question match single char")
	}
	if wildcardMatch("mvn?", "mv") {
		t.Fatal("expected no match — '?' requires a char")
	}
}

func TestEvaluateDefaultAllow(t *testing.T) {
	// No rules → default allow
	eff := Evaluate("bash", "anything")
	if eff != EffectAllow {
		t.Fatalf("expected allow, got %s", eff)
	}
}

func TestEvaluateDenyWins(t *testing.T) {
	rules := []Rule{
		{Action: "*", Resource: "*", Effect: EffectAllow},
		{Action: "bash", Resource: "*", Effect: EffectDeny},
	}
	if eff := Evaluate("bash", "ls", rules...); eff != EffectDeny {
		t.Fatalf("expected deny, got %s", eff)
	}
	if eff := Evaluate("read_file", "foo.go", rules...); eff != EffectAllow {
		t.Fatalf("expected allow, got %s", eff)
	}
}

func TestEvaluateLastMatchWins(t *testing.T) {
	rules := []Rule{
		{Action: "*", Resource: "*", Effect: EffectDeny},
		{Action: "bash", Resource: "ls *", Effect: EffectAllow},
		{Action: "bash", Resource: "*", Effect: EffectDeny},
	}
	// Last rule for bash:deny wins for "find"
	if eff := Evaluate("bash", "find .", rules...); eff != EffectDeny {
		t.Fatalf("expected deny for find, got %s", eff)
	}
	// "ls *" rule is before the last deny — does it still match?
	// Last-match-wins: the LAST rule (bash:* deny) matches.
	if eff := Evaluate("bash", "ls -la", rules...); eff != EffectDeny {
		t.Fatalf("expected deny for ls (last rule wins), got %s", eff)
	}
}

func TestEvaluateWhitelistMode(t *testing.T) {
	// Whitelist: deny all, then allow specific tools
	rules := []Rule{
		{Action: "*", Resource: "*", Effect: EffectDeny},
		{Action: "read_file", Resource: "*", Effect: EffectAllow},
	}
	if eff := Evaluate("read_file", "foo.go", rules...); eff != EffectAllow {
		t.Fatalf("expected allow for read_file, got %s", eff)
	}
	if eff := Evaluate("bash", "ls", rules...); eff != EffectDeny {
		t.Fatalf("expected deny for bash, got %s", eff)
	}
}

func TestFilterToolsDeny(t *testing.T) {
	all := []string{"bash", "read_file", "write_file", "search_files"}
	rules := []Rule{
		{Action: "bash", Resource: "*", Effect: EffectDeny},
	}
	filtered := FilterTools(all, rules...)
	expected := []string{"read_file", "write_file", "search_files"}
	if len(filtered) != len(expected) {
		t.Fatalf("got %v, want %v", filtered, expected)
	}
	for i, name := range filtered {
		if name != expected[i] {
			t.Fatalf("index %d: got %s, want %s", i, name, expected[i])
		}
	}
}

func TestFilterToolsWhitelist(t *testing.T) {
	all := []string{"bash", "read_file", "write_file", "search_files"}
	rules := []Rule{
		{Action: "*", Resource: "*", Effect: EffectDeny},
		{Action: "read_file", Resource: "*", Effect: EffectAllow},
		{Action: "search_files", Resource: "*", Effect: EffectAllow},
	}
	filtered := FilterTools(all, rules...)
	expected := []string{"read_file", "search_files"}
	if len(filtered) != len(expected) {
		t.Fatalf("got %v, want %v", filtered, expected)
	}
	for i, name := range filtered {
		if name != expected[i] {
			t.Fatalf("index %d: got %s, want %s", i, name, expected[i])
		}
	}
}
