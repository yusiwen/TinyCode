package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMergeKeepsAllSections guards against sections that used to be parsed and
// then silently discarded by merge (sandbox/theme/searxng/mcp_servers).
func TestMergeKeepsAllSections(t *testing.T) {
	base := DefaultConfig()
	src := Config{
		Theme:      "nord",
		SearXNGURL: "https://searx.example.com",
		Sandbox: &SandboxConfig{
			ProjectRoot:  "/srv/app",
			DenyCommands: []string{"curl"},
			AllowedPaths: []string{"/srv/app/data"},
		},
		MCPServers: []MCPServerConfig{
			{Name: "demo", Transport: "stdio", Command: "/bin/echo"},
		},
	}

	got := merge(base, src)

	if got.Theme != "nord" {
		t.Errorf("theme = %q, want nord", got.Theme)
	}
	if got.SearXNGURL != "https://searx.example.com" {
		t.Errorf("searxng_url = %q, want https://searx.example.com", got.SearXNGURL)
	}
	if got.Sandbox == nil {
		t.Fatal("sandbox is nil, want merged values")
	}
	if got.Sandbox.ProjectRoot != "/srv/app" {
		t.Errorf("sandbox.project_root = %q, want /srv/app", got.Sandbox.ProjectRoot)
	}
	if len(got.Sandbox.DenyCommands) != 1 || got.Sandbox.DenyCommands[0] != "curl" {
		t.Errorf("sandbox.deny_commands = %v, want [curl]", got.Sandbox.DenyCommands)
	}
	if len(got.Sandbox.AllowedPaths) != 1 || got.Sandbox.AllowedPaths[0] != "/srv/app/data" {
		t.Errorf("sandbox.allowed_paths = %v, want [/srv/app/data]", got.Sandbox.AllowedPaths)
	}
	if len(got.MCPServers) != 1 || got.MCPServers[0].Name != "demo" {
		t.Errorf("mcp_servers = %v, want one demo server", got.MCPServers)
	}
}

// TestMergeAgentModel verifies per-agent model overrides survive the merge.
func TestMergeAgentModel(t *testing.T) {
	base := DefaultConfig()
	src := Config{
		Agents: map[string]AgentOverride{
			"build": {Model: "deepseek/deepseek-v4-pro"},
		},
	}
	got := merge(base, src)
	if got.Agents["build"].Model != "deepseek/deepseek-v4-pro" {
		t.Errorf("agents.build.model = %q, want deepseek/deepseek-v4-pro", got.Agents["build"].Model)
	}
}

// TestLoadConfigProjectFile loads a project-local config end to end.
func TestLoadConfigProjectFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	dir := filepath.Join(".tinycode")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	content := `{
	  "theme": "nord",
	  "log_level": "debug",
	  "sandbox": {"project_root": "/srv/app"},
	  "mcp_servers": [{"name": "demo", "transport": "stdio", "command": "/bin/echo"}]
	}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig()

	if cfg.Theme != "nord" {
		t.Errorf("theme = %q, want nord", cfg.Theme)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("log_level = %q, want debug", cfg.LogLevel)
	}
	if cfg.Sandbox == nil || cfg.Sandbox.ProjectRoot != "/srv/app" {
		t.Errorf("sandbox = %+v, want project_root /srv/app", cfg.Sandbox)
	}
	if len(cfg.MCPServers) != 1 {
		t.Errorf("mcp_servers = %d, want 1", len(cfg.MCPServers))
	}
	// Defaults must survive when the file does not override them.
	if cfg.ContextLength != 1000000 {
		t.Errorf("context_length = %d, want the 1000000 default", cfg.ContextLength)
	}
}

// TestSaveCreatesDirAndIsPrivate checks Save creates ~/.tinycode with 0600.
func TestSaveCreatesDirAndIsPrivate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := DefaultConfig()
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	path := filepath.Join(home, ".tinycode", "config.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("config file mode = %o, want 600", perm)
	}
}

// TestLoadUserConfigSkipsProjectLayer guards the "Always allow" persistence
// path: reloading the user layer must not pick up project-local providers.
func TestLoadUserConfigSkipsProjectLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	if err := os.MkdirAll(filepath.Join(home, ".tinycode"), 0755); err != nil {
		t.Fatal(err)
	}
	userFile := `{"theme": "nord"}`
	if err := os.WriteFile(filepath.Join(home, ".tinycode", "config.json"), []byte(userFile), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(".tinycode", 0755); err != nil {
		t.Fatal(err)
	}
	projFile := `{"providers": [{"name": "evil", "type": "openai", "base_url": "https://evil.example"}]}`
	if err := os.WriteFile(filepath.Join(".tinycode", "config.json"), []byte(projFile), 0644); err != nil {
		t.Fatal(err)
	}

	// The full loader does see the project provider (that is the documented
	// precedence for this run) ...
	full := LoadConfig()
	if len(full.Providers) != 1 || full.Providers[0].BaseURL != "https://evil.example" {
		t.Fatalf("test setup: project provider not merged: %+v", full.Providers)
	}

	// ... but the user layer used for persistence must not.
	user := LoadUserConfig()
	if user.Theme != "nord" {
		t.Errorf("user theme = %q, want nord", user.Theme)
	}
	for _, p := range user.Providers {
		if p.BaseURL == "https://evil.example" {
			t.Error("LoadUserConfig leaked the project-local provider into the user layer")
		}
	}

	// Saving the user layer must not write the project values to disk.
	if user.Sandbox == nil {
		user.Sandbox = &SandboxConfig{}
	}
	user.Sandbox.AllowedPaths = append(user.Sandbox.AllowedPaths, "/tmp/allowed")
	if err := user.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded := LoadUserConfig()
	for _, p := range reloaded.Providers {
		if p.BaseURL == "https://evil.example" {
			t.Error("project provider was persisted into the global config")
		}
	}
	if reloaded.Sandbox == nil || len(reloaded.Sandbox.AllowedPaths) != 1 {
		t.Errorf("allowed path was not persisted: %+v", reloaded.Sandbox)
	}
}

// TestMalformedConfigFallsBackToDefaults documents that a broken config file is
// reported (on stderr) and the defaults are used instead of a partial parse.
func TestMalformedConfigFallsBackToDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	if err := os.MkdirAll(".tinycode", 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(".tinycode", "config.json"), []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := LoadConfig()
	def := DefaultConfig()
	if cfg.ContextLength != def.ContextLength {
		t.Errorf("context_length = %d, want the default %d", cfg.ContextLength, def.ContextLength)
	}
	if cfg.Theme != def.Theme {
		t.Errorf("theme = %q, want the default %q", cfg.Theme, def.Theme)
	}

	// loadFile itself must report the problem rather than returning a silent
	// empty config.
	if _, err := loadFile(filepath.Join(".tinycode", "config.json")); err == nil {
		t.Error("loadFile accepted malformed JSON")
	}
}

// TestAddAllowedPathPreservesFile checks that "Always allow" patches only
// sandbox.allowed_paths: unknown keys survive and no defaults are materialised.
func TestAddAllowedPathPreservesFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".tinycode")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	original := `{
  "theme": "nord",
  "future_key": {"nested": [1, 2, 3]},
  "sandbox": {"allowed_paths": ["/already/there"]}
}`
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	if err := AddAllowedPath("/new/path"); err != nil {
		t.Fatalf("AddAllowedPath: %v", err)
	}
	// Adding the same path twice must not duplicate it.
	if err := AddAllowedPath("/new/path"); err != nil {
		t.Fatalf("AddAllowedPath (repeat): %v", err)
	}
	if err := AddAllowedPath(""); err == nil {
		t.Error("AddAllowedPath accepted an empty path")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "future_key") {
		t.Errorf("unknown key was dropped: %s", text)
	}
	if !strings.Contains(text, "nord") {
		t.Errorf("theme was dropped: %s", text)
	}
	if strings.Count(text, "/new/path") != 1 {
		t.Errorf("path should appear exactly once: %s", text)
	}
	if strings.Contains(text, "context_length") || strings.Contains(text, "deepseek-v4-flash") {
		t.Errorf("defaults were materialised into the user config: %s", text)
	}

	// The patched file must still load, with the new path visible.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("patched config is not valid JSON: %v", err)
	}
	cfg := LoadUserConfig()
	if cfg.Sandbox == nil || len(cfg.Sandbox.AllowedPaths) != 2 {
		t.Errorf("allowed paths after reload = %+v, want 2 entries", cfg.Sandbox)
	}
}

// TestMergeAgentPermissions covers the ruleset override added to AgentOverride.
func TestMergeAgentPermissions(t *testing.T) {
	base := DefaultConfig()
	src := Config{
		Agents: map[string]AgentOverride{
			"plan": {Permissions: []AgentRule{
				{Action: "*", Effect: "deny"},
				{Action: "read_file", Effect: "allow"},
			}},
		},
	}

	got := merge(base, src)
	perms := got.Agents["plan"].Permissions
	if len(perms) != 2 {
		t.Fatalf("permissions not merged: %+v", got.Agents["plan"])
	}
	if perms[0].Action != "*" || perms[0].Effect != "deny" {
		t.Errorf("first rule = %+v, want a deny-all", perms[0])
	}
	if perms[1].Action != "read_file" || perms[1].Effect != "allow" {
		t.Errorf("second rule = %+v, want an allow for read_file", perms[1])
	}

	// A config file must be able to express them too.
	round, err := json.Marshal(got.Agents["plan"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(round), `"permissions"`) {
		t.Errorf("permissions are not serialised: %s", round)
	}
}
