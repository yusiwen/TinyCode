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

// TestSandboxBoundaryIgnoresTheProjectLayer is the unit half of issue #162: the
// two sandbox keys that can widen the fence are read from the user's own file,
// and the project-local one can neither set nor extend them. The same keys in
// the user file are honoured, so the rule is about the layer and not about
// refusing the keys.
func TestSandboxBoundaryIgnoresTheProjectLayer(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	if err := os.MkdirAll(filepath.Join(home, ".tinycode"), 0700); err != nil {
		t.Fatal(err)
	}
	userFile := `{"sandbox":{"project_root":"/srv/user","allowed_paths":["/srv/user-cache"]}}`
	if err := os.WriteFile(filepath.Join(home, ".tinycode", "config.json"), []byte(userFile), 0600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(".tinycode", 0755); err != nil {
		t.Fatal(err)
	}
	projFile := `{"sandbox":{"project_root":"/","allowed_paths":["/etc"]}}`
	if err := os.WriteFile(filepath.Join(".tinycode", "config.json"), []byte(projFile), 0644); err != nil {
		t.Fatal(err)
	}

	user := UserSandboxBoundary()
	if user.ProjectRoot != "/srv/user" {
		t.Errorf("user boundary root = %q, want /srv/user", user.ProjectRoot)
	}
	if len(user.AllowedPaths) != 1 || user.AllowedPaths[0] != "/srv/user-cache" {
		t.Errorf("user allowed paths = %v, want [/srv/user-cache]", user.AllowedPaths)
	}

	project := ProjectSandboxBoundary()
	if project.ProjectRoot != "/" || len(project.AllowedPaths) != 1 || project.AllowedPaths[0] != "/etc" {
		t.Errorf("project boundary = %+v, want the file's own values so a caller can report them", project)
	}

	// The merged loader still carries both layers — which is exactly why the
	// startup wiring must not read the boundary keys from it. If this ever stops
	// being true, the merge semantics changed and the rule needs re-reading.
	if merged := LoadConfig(); merged.Sandbox == nil || merged.Sandbox.ProjectRoot != "/" {
		t.Fatalf("test setup: the merged config no longer carries the project root: %+v", merged.Sandbox)
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

// boolPtr is a small helper for the pointer-valued confine_commands field.
func boolPtr(b bool) *bool { return &b }

// TestResolveConfineCommandsDefaultIsPerPlatform pins the decision issue #139
// asks for: where the host can confine a subprocess the default is on, and
// where it cannot the default is off — never a default of on that would refuse
// every command because the launcher fails closed.
func TestResolveConfineCommandsDefaultIsPerPlatform(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	noSandbox := Config{}
	if !ResolveConfineCommands(&noSandbox, true) {
		t.Error("with a mechanism and no configuration, confinement = off; want the platform default on")
	}
	if ResolveConfineCommands(&noSandbox, false) {
		t.Error("without a mechanism and no configuration, confinement = on; want the platform default off")
	}
}

// TestResolveConfineCommandsUserMayTurnItOff covers the escape hatch the issue
// requires: the user's own config may turn confinement off even where the
// default is on, while a project-local file may not.
func TestResolveConfineCommandsUserMayTurnItOff(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".tinycode"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".tinycode", "config.json"),
		[]byte(`{"sandbox":{"confine_commands":false}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	// The merged config cannot express the false (merge keeps "on" only), so
	// the resolver reads it from the user layer.
	if merged := LoadConfig(); merged.Sandbox != nil && merged.Sandbox.ConfineCommands != nil {
		t.Fatalf("test setup: the merged config already carries confine_commands = %v", *merged.Sandbox.ConfineCommands)
	}
	if ResolveConfineCommands(&Config{}, true) {
		t.Error("the user's explicit false did not turn confinement off on a capable host")
	}
	// The escape hatch is the user's, so it also beats a checked-out
	// repository's request to turn confinement on.
	projectAsked := Config{Sandbox: &SandboxConfig{ConfineCommands: boolPtr(true)}}
	if ResolveConfineCommands(&projectAsked, true) {
		t.Error("a project-local on-request overrode the user's explicit false")
	}
}

// TestResolveConfineCommandsProjectCannotForceItOnWithoutAMechanism is the veto
// the layer rule needs: the project-local file is attacker-controlled and
// merge() keeps its "on" direction, so on a host that cannot apply the boundary
// the request must not take effect — the launcher fails closed, and the session
// would refuse every command because it merely cloned that repository.
func TestResolveConfineCommandsProjectCannotForceItOnWithoutAMechanism(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	projectAsked := Config{Sandbox: &SandboxConfig{ConfineCommands: boolPtr(true)}}
	if ResolveConfineCommands(&projectAsked, false) {
		t.Error("a project-local on-request turned confinement on where the host has no mechanism; every command would be refused")
	}
	if !ResolveConfineCommands(&projectAsked, true) {
		t.Error("a project-local on-request was vetoed on a host that can apply the boundary")
	}
}

// TestMergeConfineCommandsOnlyNarrows pins the layer rule: a project-local
// config may turn confinement on but cannot turn it off, because off is the
// widening direction.
func TestMergeConfineCommandsOnlyNarrows(t *testing.T) {
	base := DefaultConfig()
	base.Sandbox = &SandboxConfig{ConfineCommands: boolPtr(true)}

	dropped := merge(base, Config{Sandbox: &SandboxConfig{ConfineCommands: boolPtr(false)}})
	if dropped.Sandbox == nil || dropped.Sandbox.ConfineCommands == nil || !*dropped.Sandbox.ConfineCommands {
		t.Fatal("an overlay turned confinement off; a project-local file must not be able to widen the fence")
	}

	raised := merge(DefaultConfig(), Config{Sandbox: &SandboxConfig{ConfineCommands: boolPtr(true)}})
	if raised.Sandbox == nil || raised.Sandbox.ConfineCommands == nil || !*raised.Sandbox.ConfineCommands {
		t.Fatal("an overlay could not turn confinement on; an overlay must be able to narrow")
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

// TestMergeBudgetLimits covers the merge rule for the cumulative token budgets:
// a positive limit is taken, an absent one leaves the base alone, and a negative
// one is not a way to switch a limit off — only zero is.
func TestMergeBudgetLimits(t *testing.T) {
	base := DefaultConfig()
	if base.Budget != nil {
		t.Fatalf("default config has a budget: %+v, want none so nothing is bounded by default", base.Budget)
	}
	if run, session := base.TokenBudgets(); run != 0 || session != 0 {
		t.Fatalf("default TokenBudgets() = %d/%d, want 0/0", run, session)
	}

	got := merge(base, Config{Budget: &BudgetConfig{MaxTokensPerRun: 5000}})
	if got.Budget == nil {
		t.Fatal("budget is nil, want the merged section")
	}
	if got.Budget.MaxTokensPerRun != 5000 {
		t.Errorf("max_tokens_per_run = %d, want 5000", got.Budget.MaxTokensPerRun)
	}
	if got.Budget.MaxTokensPerSession != 0 {
		t.Errorf("max_tokens_per_session = %d, want 0 (unset)", got.Budget.MaxTokensPerSession)
	}

	// A later layer may raise or lower a limit, but not disable one with a
	// negative value: that would read as "no limit" to a careless comparison.
	got = merge(got, Config{Budget: &BudgetConfig{MaxTokensPerRun: -1, MaxTokensPerSession: 900}})
	if got.Budget.MaxTokensPerRun != 5000 {
		t.Errorf("max_tokens_per_run = %d after a negative override, want it unchanged at 5000", got.Budget.MaxTokensPerRun)
	}
	if got.Budget.MaxTokensPerSession != 900 {
		t.Errorf("max_tokens_per_session = %d, want 900", got.Budget.MaxTokensPerSession)
	}
	if run, session := got.TokenBudgets(); run != 5000 || session != 900 {
		t.Errorf("TokenBudgets() = %d/%d, want 5000/900", run, session)
	}
}

// TestBudgetRoundTripsThroughJSON pins the file names a user types.
func TestBudgetRoundTripsThroughJSON(t *testing.T) {
	raw := []byte(`{"budget":{"max_tokens_per_run":120000,"max_tokens_per_session":2000000}}`)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if run, session := cfg.TokenBudgets(); run != 120000 || session != 2000000 {
		t.Errorf("TokenBudgets() = %d/%d, want 120000/2000000", run, session)
	}
}

// TestMergePricingRates covers the merge rule for declared rates: the currency is
// taken from the layer that sets it, and rates merge per key so a project file can
// price one route without dropping the ones the global file declared.
func TestMergePricingRates(t *testing.T) {
	base := merge(DefaultConfig(), Config{Pricing: &PricingConfig{
		Currency: "USD",
		Prices: map[string]PriceEntry{
			"deepseek/*": {InputPerMillion: 0.3, OutputPerMillion: 1.2},
		},
	}})
	if base.Pricing == nil || base.Pricing.Currency != "USD" {
		t.Fatalf("pricing after the first layer = %+v, want USD", base.Pricing)
	}

	got := merge(base, Config{Pricing: &PricingConfig{
		Prices: map[string]PriceEntry{
			"openrouter/*": {InputPerMillion: 3},
		},
	}})
	if len(got.Pricing.Prices) != 2 {
		t.Fatalf("prices = %v, want both keys kept", got.Pricing.Prices)
	}
	if got.Pricing.Prices["deepseek/*"].InputPerMillion != 0.3 {
		t.Errorf("deepseek/* = %v, want the earlier layer kept", got.Pricing.Prices["deepseek/*"])
	}
	if got.Pricing.Prices["openrouter/*"].InputPerMillion != 3 {
		t.Errorf("openrouter/* = %v, want the later layer added", got.Pricing.Prices["openrouter/*"])
	}
	if got.Pricing.Currency != "USD" {
		t.Errorf("currency = %q, want USD: a layer that sets none must not clear it", got.Pricing.Currency)
	}

	// A later layer replaces the rate it names.
	got = merge(got, Config{Pricing: &PricingConfig{
		Currency: "EUR",
		Prices:   map[string]PriceEntry{"deepseek/*": {InputPerMillion: 9}},
	}})
	if got.Pricing.Prices["deepseek/*"].InputPerMillion != 9 {
		t.Errorf("deepseek/* = %v, want the later rate", got.Pricing.Prices["deepseek/*"])
	}
	if got.Pricing.Currency != "EUR" {
		t.Errorf("currency = %q, want EUR", got.Pricing.Currency)
	}
}

// TestPricingRoundTripsThroughJSON pins the key names a user types, including the
// route/model key with a model name that itself contains a separator.
func TestPricingRoundTripsThroughJSON(t *testing.T) {
	raw := []byte(`{"providers":[{"name":"openrouter","type":"openai","model":"m","cost_currency":"credits"}],` +
		`"pricing":{"currency":"USD","prices":{"openrouter/anthropic/claude-sonnet-4":{"input_per_million":3,"output_per_million":15,"cache_read_per_million":0.3}}}}`)
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Providers[0].CostCurrency != "credits" {
		t.Errorf("cost_currency = %q, want credits", cfg.Providers[0].CostCurrency)
	}
	entry, ok := cfg.Pricing.Prices["openrouter/anthropic/claude-sonnet-4"]
	if !ok {
		t.Fatalf("prices = %v, want the route/model key", cfg.Pricing.Prices)
	}
	if entry.CacheReadPerMillion != 0.3 {
		t.Errorf("cache_read_per_million = %v, want 0.3", entry.CacheReadPerMillion)
	}
}
