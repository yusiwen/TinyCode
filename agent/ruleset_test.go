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

// TestTranslateToolLists covers the config-to-ruleset translation used for
// per-agent allowed_tools/denied_tools overrides.
func TestTranslateToolLists(t *testing.T) {
	allowAll := Ruleset{{Action: "*", Resource: "*", Effect: EffectAllow}}
	readOnly := Ruleset{
		{Action: "*", Resource: "*", Effect: EffectDeny},
		{Action: "read_file", Resource: "*", Effect: EffectAllow},
		{Action: "search_files", Resource: "*", Effect: EffectAllow},
	}

	t.Run("denied adds to the base ruleset", func(t *testing.T) {
		rules := TranslateToolLists(allowAll, nil, []string{"bash"})
		if Evaluate("bash", "*", rules...) != EffectDeny {
			t.Error("bash should be denied")
		}
		if Evaluate("write_file", "*", rules...) != EffectAllow {
			t.Error("write_file should stay allowed")
		}
	})

	t.Run("allowed acts as a whitelist", func(t *testing.T) {
		rules := TranslateToolLists(allowAll, []string{"read_file"}, nil)
		if Evaluate("read_file", "*", rules...) != EffectAllow {
			t.Error("read_file should be allowed")
		}
		if Evaluate("bash", "*", rules...) != EffectDeny {
			t.Error("bash should be denied by the whitelist")
		}
		if Evaluate("write_file", "*", rules...) != EffectDeny {
			t.Error("write_file should be denied by the whitelist")
		}
	})

	t.Run("denied wins over allowed", func(t *testing.T) {
		rules := TranslateToolLists(readOnly, []string{"read_file", "bash"}, []string{"bash"})
		if Evaluate("bash", "*", rules...) != EffectDeny {
			t.Error("bash is in both lists and must end up denied")
		}
		if Evaluate("read_file", "*", rules...) != EffectAllow {
			t.Error("read_file should be allowed")
		}
	})

	t.Run("base ruleset is preserved and not mutated", func(t *testing.T) {
		base := Ruleset{
			{Action: "*", Resource: "*", Effect: EffectAllow},
			{Action: "task", Resource: "*", Effect: EffectDeny},
		}
		rules := TranslateToolLists(base, nil, []string{"bash"})
		if Evaluate("task", "*", rules...) != EffectDeny {
			t.Error("base deny rule for task was lost")
		}
		if len(base) != 2 {
			t.Errorf("base ruleset was mutated: %v", base)
		}
	})

	t.Run("nil lists keep the base behaviour", func(t *testing.T) {
		rules := TranslateToolLists(readOnly, nil, nil)
		if Evaluate("bash", "*", rules...) != EffectDeny {
			t.Error("base deny-all must survive an empty translation")
		}
		if Evaluate("read_file", "*", rules...) != EffectAllow {
			t.Error("base allow must survive an empty translation")
		}
	})

	t.Run("empty allowed list means no override", func(t *testing.T) {
		rules := TranslateToolLists(readOnly, []string{}, nil)
		if Evaluate("read_file", "*", rules...) != EffectAllow {
			t.Error("an empty allowed list must not deny every tool")
		}
	})

	t.Run("whitelist cannot re-grant a named base deny", func(t *testing.T) {
		// Shaped like the "general" sub-agent: allow all, but deny delegation.
		base := Ruleset{
			{Action: "*", Resource: "*", Effect: EffectAllow},
			{Action: "task", Resource: "*", Effect: EffectDeny},
			{Action: "skill_manage", Resource: "*", Effect: EffectDeny},
		}
		rules := TranslateToolLists(base, []string{"*"}, nil)
		if Evaluate("bash", "*", rules...) != EffectAllow {
			t.Error("bash should remain allowed")
		}
		if Evaluate("task", "*", rules...) != EffectDeny {
			t.Error("a configured whitelist must not re-grant task")
		}
		if Evaluate("skill_manage", "*", rules...) != EffectDeny {
			t.Error("a configured whitelist must not re-grant skill_manage")
		}
	})
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

// TestPlanAgentLSPToolNames guards the allow-list against tool-name drift: the
// names must match lsp.ToolType values exactly, or plan mode silently loses the
// LSP tools.
func TestPlanAgentLSPToolNames(t *testing.T) {
	plan := DefaultAgents()["plan"]
	if plan == nil {
		t.Fatal("plan agent missing")
	}
	for _, name := range []string{"lsp_definition", "lsp_references", "lsp_hover", "lsp_symbols"} {
		if !ToolAllowedFor(plan, name) {
			t.Errorf("plan mode should allow %q", name)
		}
	}
	for _, name := range []string{"lsp_go_to_definition", "lsp_find_references", "lsp_document_symbols"} {
		if ToolAllowedFor(plan, name) {
			t.Errorf("plan mode still references the non-existent tool name %q", name)
		}
	}
}
