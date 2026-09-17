package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
)

func TestLoadProjectContextNoFile(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	ctx := loadProjectContext()
	if ctx != "" {
		t.Errorf("expected empty for no file, got %q", ctx)
	}
}

func TestLoadProjectContextAGENTSMD(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	content := "This project uses Go 1.24 and follows standard Go conventions."
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(content), 0644)

	ctx := loadProjectContext()
	if ctx != content {
		t.Errorf("expected AGENTS.md content, got %q", ctx)
	}
}

func TestLoadProjectContextCLAUDEMD(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	content := "Always run tests before committing."
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(content), 0644)

	ctx := loadProjectContext()
	if ctx != content {
		t.Errorf("expected CLAUDE.md content, got %q", ctx)
	}
}

func TestLoadProjectContextPrecedence(t *testing.T) {
	dir := t.TempDir()
	oldDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldDir)

	// Both files exist — AGENTS.md takes precedence
	os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("agents content"), 0644)
	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("claude content"), 0644)

	ctx := loadProjectContext()
	if ctx != "agents content" {
		t.Errorf("expected AGENTS.md content (first match), got %q", ctx)
	}
}

// TestInfoCommandsDoNotTouchSessions is the regression test for the data-loss
// bug where --list-sessions (and the other informational commands) created and
// flushed an empty "default" session, overwriting a saved conversation.
func TestInfoCommandsDoNotTouchSessions(t *testing.T) {
	// Isolate the user config and log dir from the developer's real HOME.
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	dir := t.TempDir()
	saved := `{"id":"default","title":"precious","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","message_count":1,"messages":[{"role":"user","content":"do not lose me"}]}`
	path := filepath.Join(dir, "default.json")
	if err := os.WriteFile(path, []byte(saved), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--list-sessions", "--session-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--list-sessions failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("saved session disappeared: %v", err)
	}
	if !strings.Contains(string(data), "precious") || !strings.Contains(string(data), "do not lose me") {
		t.Errorf("saved session was overwritten: %s", data)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("informational command created files: %v", names)
	}
}

// TestExpandPath covers the "~" and "$VAR" expansion used for configured paths.
func TestExpandPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("TINYCODE_TEST_DIR", "/tmp/tinycode-expand")

	cases := []struct{ in, want string }{
		{"~/.tinycode/sessions", filepath.Join(home, ".tinycode/sessions")},
		{"~", home},
		{"$TINYCODE_TEST_DIR/logs", "/tmp/tinycode-expand/logs"},
		{"/absolute/path", "/absolute/path"},
		{"relative/path", "relative/path"},
		{"~notahome/x", "~notahome/x"},
	}
	for _, tc := range cases {
		if got := expandPath(tc.in); got != tc.want {
			t.Errorf("expandPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestExportSessionWritesPrivateMarkdown covers --export-session end to end:
// the transcript is written next to the session file's name and is not
// world-readable.
func TestExportSessionWritesPrivateMarkdown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	t.Chdir(work)

	dir := t.TempDir()
	sess := `{"id":"exp","title":"exported","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z","message_count":2,"messages":[{"role":"user","content":"hello there"},{"role":"assistant","content":"hi"}]}`
	if err := os.WriteFile(filepath.Join(dir, "exp.json"), []byte(sess), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--export-session", "exp", "--session-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--export-session failed: %v", err)
	}

	out := filepath.Join(work, "exp.md")
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("export missing: %v", err)
	}
	if !strings.Contains(string(data), "hello there") || !strings.Contains(string(data), "hi") {
		t.Errorf("export does not contain the conversation:\n%s", data)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("export mode = %o, want 600", perm)
	}
}

// TestDeleteSessionCommand covers the --delete-session path (and that a
// traversal id is refused rather than deleting something else).
func TestDeleteSessionCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gone.json"), []byte(`{"id":"gone"}`), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--delete-session", "gone", "--session-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--delete-session failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.json")); err == nil {
		t.Error("session was not deleted")
	}

	// A traversal id must be rejected by the store, not acted on.
	cmd = newRootCmd()
	cmd.SetArgs([]string{"--delete-session", "../escape", "--session-dir", dir})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Error("expected a traversal session id to be rejected")
	}
}

// TestInvalidAgentPermissionEffectIsRejected checks that a bad effect in a
// configured ruleset fails loudly instead of being silently ignored.
func TestInvalidAgentPermissionEffectIsRejected(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	if err := os.MkdirAll(".tinycode", 0755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"agents": {"plan": {"permissions": [{"action": "bash", "effect": "banana"}]}}}`
	if err := os.WriteFile(filepath.Join(".tinycode", "config.json"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--list-sessions", "--session-dir", t.TempDir()})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for an unknown permission effect")
	}
	if !strings.Contains(err.Error(), "banana") {
		t.Errorf("error should name the bad effect, got: %v", err)
	}
}

// TestResolveProviderKeyPrecedence pins the API key lookup order: explicit
// api_key_env, then NAME_API_KEY, then the generic OPENAI_API_KEY fallback, with
// the --api-key flag overriding the primary provider only.
func TestResolveProviderKeyPrecedence(t *testing.T) {
	env := map[string]string{
		"DS_API_KEY":     "from-derived",
		"EXPLICIT_KEY":   "from-explicit",
		"OPENAI_API_KEY": "from-fallback",
	}
	getenv := func(k string) string { return env[k] }

	tests := []struct {
		name    string
		pc      config.ProviderRecordConfig
		primary bool
		flag    string
		want    string
	}{
		{"derived NAME_API_KEY", config.ProviderRecordConfig{Name: "ds"}, false, "", "from-derived"},
		{"explicit env wins", config.ProviderRecordConfig{Name: "ds", APIKeyEnv: "EXPLICIT_KEY"}, false, "", "from-explicit"},
		{"generic fallback", config.ProviderRecordConfig{Name: "nokey"}, false, "", "from-fallback"},
		{"explicit env disables fallback", config.ProviderRecordConfig{Name: "ds", APIKeyEnv: "MISSING_KEY"}, false, "", ""},
		{"flag overrides primary", config.ProviderRecordConfig{Name: "ds"}, true, "from-flag", "from-flag"},
		{"flag ignored for secondary", config.ProviderRecordConfig{Name: "ds"}, false, "from-flag", "from-derived"},
	}
	for _, tt := range tests {
		if got := resolveProviderKey(tt.pc, tt.primary, tt.flag, getenv); got != tt.want {
			t.Errorf("%s: resolveProviderKey = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestProviderRuntimeOverrides verifies model/base URL resolution and the
// DeepSeek default endpoint.
func TestProviderRuntimeOverrides(t *testing.T) {
	pc := config.ProviderRecordConfig{Model: "configured-model", BaseURL: "https://configured.example"}

	model, base := providerRuntime(pc, true, "flag-model", "https://flag.example")
	if model != "flag-model" || base != "https://flag.example" {
		t.Fatalf("primary got (%q, %q), want the CLI overrides", model, base)
	}

	model, base = providerRuntime(pc, false, "flag-model", "https://flag.example")
	if model != "configured-model" || base != "https://configured.example" {
		t.Fatalf("secondary got (%q, %q), want the configured values", model, base)
	}

	if _, base := providerRuntime(config.ProviderRecordConfig{}, false, "", ""); base != "https://api.deepseek.com" {
		t.Fatalf("empty base URL = %q, want the DeepSeek endpoint", base)
	}
}

// TestBuildProviderRegistry covers provider construction: type routing and the
// single default provider used when nothing is configured.
func TestBuildProviderRegistry(t *testing.T) {
	reg := buildProviderRegistry(&config.Config{}, "", "", "")
	if reg.Len() != 1 {
		t.Fatalf("empty config produced %d providers, want 1", reg.Len())
	}
	if !strings.HasPrefix(reg.CurrentName(), "default") {
		t.Fatalf("default provider name = %q", reg.CurrentName())
	}

	cfg := &config.Config{Providers: []config.ProviderRecordConfig{
		{Name: "remote", Type: "openai", Model: "m"},
		{Name: "local", Type: "ollama", Model: "llama"},
	}}
	reg = buildProviderRegistry(cfg, "", "", "")
	if reg.Len() != 2 {
		t.Fatalf("registry has %d providers, want 2", reg.Len())
	}
	records := reg.List()
	if records[0].Name != "remote" || records[1].Name != "local" {
		t.Fatalf("provider order = %q, %q", records[0].Name, records[1].Name)
	}
	if _, ok := records[0].Provider.(*agent.OpenAIProvider); !ok {
		t.Errorf("openai provider is %T", records[0].Provider)
	}
	if _, ok := records[1].Provider.(*agent.OllamaProvider); !ok {
		t.Errorf("ollama provider is %T", records[1].Provider)
	}
}

// TestApplyAgentOverrides verifies how config overrides reach the registry:
// explicit rulesets win, legacy tool lists are translated, unknown agents are
// ignored and a bad effect is an error.
func TestApplyAgentOverrides(t *testing.T) {
	cfg := &config.Config{Agents: map[string]config.AgentOverride{
		"build": {
			MaxSteps:     7,
			SystemPrompt: "be terse",
			Model:        "deepseek/deepseek-v4-pro",
			Permissions: []config.AgentRule{
				{Action: "bash", Resource: "git *", Effect: "deny"},
			},
		},
		"explore": {
			AllowedTools: []string{"read_file"},
			DeniedTools:  []string{"bash"},
		},
		"no-such-agent": {MaxSteps: 99},
	}}

	reg := agent.NewRegistry()
	if err := applyAgentOverrides(reg, cfg); err != nil {
		t.Fatalf("applyAgentOverrides: %v", err)
	}

	build, err := reg.Get("build")
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if build.MaxSteps != 7 || build.SystemPrompt != "be terse" {
		t.Fatalf("build = %+v, want the configured steps and prompt", build)
	}
	if build.Model != "deepseek-v4-pro" {
		t.Fatalf("build model = %q, want the model part of provider/model", build.Model)
	}
	if len(build.Permissions) != 1 {
		t.Fatalf("build permissions = %#v, want exactly the configured ruleset", build.Permissions)
	}
	rule := build.Permissions[0]
	if rule.Action != "bash" || rule.Resource != "git *" || rule.Effect != agent.EffectDeny {
		t.Fatalf("build rule = %+v", rule)
	}

	explore, err := reg.Get("explore")
	if err != nil {
		t.Fatalf("get explore: %v", err)
	}
	var blanketDeny, allowRead, denyBash bool
	for _, r := range explore.Permissions {
		switch {
		case r.Action == "*" && r.Effect == agent.EffectDeny:
			blanketDeny = true
		case r.Action == "read_file" && r.Effect == agent.EffectAllow:
			allowRead = true
		case r.Action == "bash" && r.Effect == agent.EffectDeny:
			denyBash = true
		}
	}
	if !blanketDeny || !allowRead || !denyBash {
		t.Fatalf("explore permissions = %#v, want a whitelist plus the bash deny", explore.Permissions)
	}

	bad := &config.Config{Agents: map[string]config.AgentOverride{
		"build": {Permissions: []config.AgentRule{{Action: "bash", Effect: "maybe"}}},
	}}
	if err := applyAgentOverrides(agent.NewRegistry(), bad); err == nil {
		t.Fatal("expected an error for an unknown permission effect")
	}
}
