package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// TestRegistryLifecycle covers the named-agent registry that backs Tab
// switching and the config's per-agent overrides: lookup, cycling, explicit
// selection and the guard that keeps subagents out of the session seat.
func TestRegistryLifecycle(t *testing.T) {
	r := NewRegistry()

	if got := r.CurrentName(); got != "plan" {
		t.Fatalf("initial agent = %q, want plan", got)
	}
	build, err := r.Get("build")
	if err != nil {
		t.Fatalf("Get(build): %v", err)
	}
	if build.Mode != AgentModePrimary {
		t.Fatalf("build mode = %q, want primary", build.Mode)
	}
	if _, err := r.Get("no-such-agent"); err == nil {
		t.Fatal("Get(unknown) must fail")
	}
	if got, want := len(r.List()), len(DefaultAgents()); got != want {
		t.Fatalf("List returned %d agents, want %d", got, want)
	}

	// Switch cycles through visible primary agents only.
	for i := 0; i < len(r.order)+1; i++ {
		name := r.Switch()
		cfg, err := r.Get(name)
		if err != nil {
			t.Fatalf("Switch returned unknown agent %q", name)
		}
		if cfg.Mode != AgentModePrimary || cfg.Hidden {
			t.Fatalf("Switch landed on %q, which is %s/hidden=%v", name, cfg.Mode, cfg.Hidden)
		}
	}

	if err := r.Set("build"); err != nil {
		t.Fatalf("Set(build): %v", err)
	}
	if r.CurrentName() != "build" {
		t.Fatalf("CurrentName = %q, want build", r.CurrentName())
	}
	if err := r.Set("no-such-agent"); err == nil {
		t.Fatal("Set(unknown) must fail")
	}
	// Subagents and hidden agents may not become the session agent.
	for _, name := range []string{"explore", "general", "compact", "title"} {
		cfg, err := r.Get(name)
		if err != nil {
			t.Fatalf("Get(%s): %v", name, err)
		}
		if cfg.Mode == AgentModePrimary && !cfg.Hidden {
			continue
		}
		if err := r.Set(name); err == nil {
			t.Errorf("Set(%s) succeeded for a %s agent (hidden=%v)", name, cfg.Mode, cfg.Hidden)
		}
	}

	// Register replaces in place without duplicating the entry.
	before := len(r.List())
	r.Register(AgentConfig{Name: "build", Mode: AgentModePrimary, MaxSteps: 3})
	if got := len(r.List()); got != before {
		t.Fatalf("Register duplicated the entry: %d -> %d", before, got)
	}
	if cfg, _ := r.Get("build"); cfg.MaxSteps != 3 {
		t.Fatalf("Register did not replace the config: %+v", cfg)
	}
}

// TestToolAllowedRules covers both permission paths: an explicit ruleset wins
// over the legacy tool lists, a denied tool stays denied, and a whitelist
// excludes everything it does not name.
func TestToolAllowedRules(t *testing.T) {
	ruleset := &AgentConfig{
		Permissions: Ruleset{
			{Action: "*", Resource: "*", Effect: EffectAllow},
			{Action: "bash", Resource: "*", Effect: EffectDeny},
		},
		AllowedTools: []string{"bash"},
	}
	if ruleset.IsToolAllowed("bash") {
		t.Error("the ruleset deny must win over the legacy allow list")
	}
	if !ruleset.IsToolAllowed("read_file") {
		t.Error("a tool matched only by the allow-all rule must stay allowed")
	}

	legacy := &AgentConfig{AllowedTools: []string{"read_file", "edit"}, DeniedTools: []string{"edit"}}
	if legacy.IsToolAllowed("edit") {
		t.Error("a denied tool must stay denied")
	}
	if !legacy.IsToolAllowed("read_file") {
		t.Error("a whitelisted tool must be allowed")
	}
	if legacy.IsToolAllowed("bash") {
		t.Error("a whitelist must exclude tools it does not name")
	}

	open := &AgentConfig{}
	if !open.IsToolAllowed("bash") {
		t.Error("without any list every tool is allowed")
	}

	if !ToolAllowedFor(nil, "bash") {
		t.Error("a nil config must be allowed instead of panicking")
	}
	if ToolAllowedFor(&AgentConfig{AllowedTools: []string{"*"}}, "anything") != true {
		t.Error("AllowedTools [*] must allow every tool")
	}
	if ToolAllowedFor(&AgentConfig{DeniedTools: []string{"bash"}}, "bash") != false {
		t.Error("DeniedTools must reject the tool")
	}

	r := NewRegistry()
	if err := r.Set("build"); err != nil {
		t.Fatalf("Set(build): %v", err)
	}
	buildCfg, err := r.Get("build")
	if err != nil {
		t.Fatalf("Get(build): %v", err)
	}
	if got, want := r.ToolAllowed("read_file"), buildCfg.IsToolAllowed("read_file"); got != want {
		t.Fatalf("Registry.ToolAllowed(read_file) = %v, want %v", got, want)
	}
}

// TestProviderRegistrySwitching covers the provider list the Tab key rotates
// through, including the nil-registry guards used before configuration loads.
func TestProviderRegistrySwitching(t *testing.T) {
	alpha := &MockProvider{name: "alpha"}
	beta := &MockProvider{name: "beta"}
	r := NewProviderRegistry([]ProviderRecord{
		{Name: "alpha", Provider: alpha},
		{Name: "beta", Provider: beta},
	})

	if r.Len() != 2 || r.CurrentIndex() != 0 || r.Current() != alpha {
		t.Fatalf("initial state: len=%d idx=%d current=%v", r.Len(), r.CurrentIndex(), r.Current())
	}
	if got := r.CurrentName(); got != "alpha (alpha)" {
		t.Fatalf("CurrentName = %q, want %q", got, "alpha (alpha)")
	}
	if len(r.List()) != 2 {
		t.Fatalf("List returned %d entries, want 2", len(r.List()))
	}

	if err := r.SwitchTo(1); err != nil {
		t.Fatalf("SwitchTo(1): %v", err)
	}
	if r.Current() != beta || r.CurrentIndex() != 1 {
		t.Fatalf("after SwitchTo(1): idx=%d current=%v", r.CurrentIndex(), r.Current())
	}
	if err := r.SwitchTo(2); err == nil {
		t.Error("SwitchTo(out of range) must fail")
	}
	if err := r.SwitchTo(-1); err == nil {
		t.Error("SwitchTo(-1) must fail")
	}

	if err := r.SwitchToName("alpha"); err != nil {
		t.Fatalf("SwitchToName(alpha): %v", err)
	}
	if r.CurrentIndex() != 0 {
		t.Fatalf("SwitchToName left index at %d", r.CurrentIndex())
	}
	if err := r.SwitchToName("gamma"); err == nil {
		t.Error("SwitchToName(unknown) must fail")
	}

	var nilReg *ProviderRegistry
	if nilReg.Current() != nil || nilReg.Len() != 0 || nilReg.List() != nil {
		t.Error("a nil registry must look empty")
	}
	if nilReg.CurrentName() != "none" || nilReg.CurrentIndex() != 0 {
		t.Errorf("nil registry reported name %q index %d", nilReg.CurrentName(), nilReg.CurrentIndex())
	}
	if err := nilReg.SwitchTo(0); err == nil {
		t.Error("a nil registry must reject SwitchTo")
	}
	if err := nilReg.SwitchToName("x"); err == nil {
		t.Error("a nil registry must reject SwitchToName")
	}
	if empty := NewProviderRegistry(nil); empty.Current() != nil || empty.CurrentName() != "none" {
		t.Error("an empty registry must have no current provider")
	}
}

// TestNewAgentDefaults and the mock helpers document the values the agent loop
// starts from.
func TestNewAgentDefaults(t *testing.T) {
	provider := &MockProvider{name: "p"}
	a := New(provider)
	if a.Provider != provider {
		t.Error("New must keep the given provider")
	}
	if a.MaxSteps != 20 || a.MaxTokens != 4096 {
		t.Fatalf("defaults = %d steps / %d tokens, want 20 / 4096", a.MaxSteps, a.MaxTokens)
	}
	if !strings.Contains(a.SystemPrompt, "TinyCode") {
		t.Errorf("system prompt = %q, want the default persona", a.SystemPrompt)
	}
	a.AddTool(Tool{Name: "demo"})
	if len(a.Tools) != 1 || a.Tools[0].Name != "demo" {
		t.Fatalf("AddTool did not register the tool: %+v", a.Tools)
	}
}

// TestMockHelpers covers the scripted LLM and tool doubles the other agent tests
// rely on, so a change to them cannot silently weaken those tests.
func TestMockHelpers(t *testing.T) {
	llm := NewMockLLM([]MockStep{{Content: "first"}, {Content: "second"}})
	if llm.Name() != "mock-llm" {
		t.Fatalf("MockLLM.Name = %q", llm.Name())
	}
	resp, err := llm.Chat(context.Background(), types.ChatRequest{})
	if err != nil || resp.Content != "first" {
		t.Fatalf("first Chat = %+v, %v", resp, err)
	}
	if llm.CallCount() != 1 {
		t.Fatalf("CallCount = %d, want 1", llm.CallCount())
	}
	// Once the script is exhausted the mock keeps answering instead of failing.
	if resp, err := llm.Chat(context.Background(), types.ChatRequest{}); err != nil || resp.Content != "second" {
		t.Fatalf("second Chat = %+v, %v", resp, err)
	}
	if resp, err := llm.Chat(context.Background(), types.ChatRequest{}); err != nil || !strings.Contains(resp.Content, "exhausted") {
		t.Fatalf("exhausted Chat = %+v, %v", resp, err)
	}

	tool := &MockTool{Name: "demo", Result: "result"}
	def := tool.ToolDef()
	if def.Name != "demo" || !strings.Contains(def.Description, "demo") || def.Parameters == nil {
		t.Fatalf("ToolDef = %+v", def)
	}
	if out, err := tool.Execute(context.Background(), nil); err != nil || out != "result" {
		t.Fatalf("Execute = %q, %v", out, err)
	}
	asTool := tool.ToTool()
	if asTool.Name != "demo" || asTool.Parameters == nil {
		t.Fatalf("ToTool = %+v", asTool)
	}
	if out, err := asTool.Execute(context.Background(), nil); err != nil || out != "result" {
		t.Fatalf("wrapped Execute = %q, %v", out, err)
	}

	if resp, err := (&MockProvider{}).Chat(context.Background(), types.ChatRequest{}); err != nil || resp.Content != "mock response" {
		t.Fatalf("MockProvider.Chat = %+v, %v", resp, err)
	}
}
