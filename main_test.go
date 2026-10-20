package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/types"
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

// TestPersistedGrantIsHonouredOnTheNextStart is the regression test for the gap
// issue #155's scenario exposed: "Always allow" recorded a persistent grant that
// no start read back, so the answer lasted exactly as long as the process that
// gave it. The negative control runs first — a path the sandbox already allowed
// would make the positive half prove nothing.
//
// This pins the loading rule; the call site in the startup path is pinned by
// tui/testdata/scenarios/permission-allow-always.scenario, which starts the real
// binary twice and fails if the second start asks again.
func TestPersistedGrantIsHonouredOnTheNextStart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".tinycode")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// The grant names a real path outside the project the run uses; the sandbox
	// caches both the requested and the OS-resolved form (on macOS /tmp is a
	// symlink), so either spelling must be honoured.
	granted := filepath.Join(t.TempDir(), "granted-outside-the-project.txt")
	body := fmt.Sprintf(`{"sandbox":{"allowed_path_grants":[{"path":%q,"project":"/repo"}]}}`, granted)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	previousRoot := tool.DefaultSandbox.ProjectRoot
	tool.DefaultSandbox.ProjectRoot = t.TempDir()
	tool.DefaultSandbox.ResetAllowed()
	t.Cleanup(func() {
		tool.DefaultSandbox.ResetAllowed()
		tool.DefaultSandbox.ProjectRoot = previousRoot
	})

	if err := tool.DefaultSandbox.CheckPath(granted); err == nil {
		t.Fatalf("%s is allowed before any grant was loaded; this test would prove nothing", granted)
	}
	if err := loadPersistentGrants(config.LoadLayers().User); err != nil {
		t.Fatalf("loadPersistentGrants: %v", err)
	}
	if err := tool.DefaultSandbox.CheckPath(granted); err != nil {
		t.Fatalf("the persisted grant was not honoured on the next start: %v", err)
	}
	// Loading grants must not open the fence around them: a path nobody granted
	// stays refused.
	if err := tool.DefaultSandbox.CheckPath(filepath.Join(filepath.Dir(granted), "never-granted.txt")); err == nil {
		t.Error("a path that was never granted is allowed")
	}
}

// TestProjectLocalConfigCannotGrantAPath pins the half of the loading rule that
// is a security property: grants are read from the user's own config file only.
// A checked-out repository can ship ./.tinycode/config.json, and a project that
// could grant itself a path outside its own root would turn "open this repo"
// into an escape from the fence.
func TestProjectLocalConfigCannotGrantAPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // the user's config grants nothing
	work := t.TempDir()
	t.Chdir(work)

	granted := filepath.Join(t.TempDir(), "granted-by-the-repository.txt")
	body := fmt.Sprintf(`{"sandbox":{"allowed_path_grants":[{"path":%q,"project":"/repo"}]}}`, granted)
	if err := os.MkdirAll(filepath.Join(work, ".tinycode"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, ".tinycode", "config.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	previousRoot := tool.DefaultSandbox.ProjectRoot
	tool.DefaultSandbox.ProjectRoot = t.TempDir()
	tool.DefaultSandbox.ResetAllowed()
	t.Cleanup(func() {
		tool.DefaultSandbox.ResetAllowed()
		tool.DefaultSandbox.ProjectRoot = previousRoot
	})

	if err := loadPersistentGrants(config.LoadLayers().User); err != nil {
		t.Fatalf("loadPersistentGrants: %v", err)
	}
	if err := tool.DefaultSandbox.CheckPath(granted); err == nil {
		t.Errorf("a repository-local config granted itself %s", granted)
	}
}

// restoreSandbox resets the process-wide sandbox state the startup wiring
// touches, and puts it back when the test ends.
func restoreSandbox(t *testing.T) {
	t.Helper()
	previousRoot := tool.DefaultSandbox.ProjectRoot
	previousAuto := tool.DefaultSandbox.AutoAllowPaths
	previousCache := tool.DefaultSandbox.CacheRoots
	tool.DefaultSandbox.ResetAllowed()
	t.Cleanup(func() {
		tool.DefaultSandbox.ResetAllowed()
		tool.DefaultSandbox.ProjectRoot = previousRoot
		tool.DefaultSandbox.AutoAllowPaths = previousAuto
		tool.DefaultSandbox.CacheRoots = previousCache
	})
}

// TestProjectLocalConfigCannotWidenTheFence is the wiring half of issue #162: a
// repository that asks for project_root "/" and an allowed path outside itself
// gets neither, so a path outside the real project is still refused. The control
// — a path inside the project — must stay allowed, or a refusal would only prove
// that everything is refused.
func TestProjectLocalConfigCannotWidenTheFence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".tinycode"), 0700); err != nil {
		t.Fatal(err)
	}

	project := t.TempDir()
	t.Chdir(project)
	if err := os.MkdirAll(filepath.Join(project, ".tinycode"), 0755); err != nil {
		t.Fatal(err)
	}
	body := `{"sandbox":{"project_root":"/","allowed_paths":["/etc"]}}`
	if err := os.WriteFile(filepath.Join(project, ".tinycode", "config.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	restoreSandbox(t)
	installSandboxBoundary(config.LoadLayers())

	if got := tool.DefaultSandbox.ProjectRoot; got == "/" || got == "" {
		t.Errorf("boundary root = %q; the project-local config moved it", got)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := tool.DefaultSandbox.CheckPath(filepath.Join(cwd, "inside.txt")); err != nil {
		t.Errorf("a path inside the project is refused (%v): the control half of the assertion fails", err)
	}
	if err := tool.DefaultSandbox.CheckPath(outside); err == nil {
		t.Errorf("%s is allowed: the project-local config widened the fence", outside)
	}
}

// TestUserConfigMayWidenTheFence is the other column of issue #162's table: the
// same keys in the user's own file are honoured, so the rule is about which
// layer may widen the fence, not about refusing the keys.
func TestUserConfigMayWidenTheFence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".tinycode"), 0700); err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(t.TempDir(), "allowed-by-the-user.txt")
	if err := os.WriteFile(allowed, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"sandbox":{"allowed_paths":[%q]}}`, allowed)
	if err := os.WriteFile(filepath.Join(home, ".tinycode", "config.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(t.TempDir())
	restoreSandbox(t)
	installSandboxBoundary(config.LoadLayers())

	if err := tool.DefaultSandbox.CheckPath(allowed); err != nil {
		t.Errorf("a path the user's own config allowed is refused: %v", err)
	}
}

// captureStderr runs fn with os.Stderr redirected into a pipe and returns what
// was written to it.
//
// The config loaders report a malformed file with fmt.Fprintf on os.Stderr
// rather than through the cobra command, so a pipe is the only place the
// diagnostic can be counted (issue #231).
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = write
	defer func() { os.Stderr = previous }()
	fn()
	os.Stderr = previous
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	message, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	return string(message)
}

// TestMalformedUserConfigIsReportedOnce is issue #231's acceptance at the CLI
// level: a broken ~/.tinycode/config.json costs one line naming the path, and the
// command still succeeds on the defaults.
func TestMalformedUserConfigIsReportedOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())

	dir := filepath.Join(home, ".tinycode")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"providers": `), 0600); err != nil {
		t.Fatal(err)
	}

	var runErr error
	message := captureStderr(t, func() {
		cmd := newRootCmd()
		cmd.SetArgs([]string{"--list-sessions", "--session-dir", t.TempDir()})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		runErr = cmd.Execute()
	})
	if runErr != nil {
		t.Fatalf("a malformed user config must not fail the command: %v", runErr)
	}
	if got := strings.Count(message, "ignoring"); got != 1 {
		t.Errorf("the malformed user config was reported %d times, want 1: %s", got, message)
	}
	if !strings.Contains(message, path) {
		t.Errorf("the diagnostic does not name %s: %s", path, message)
	}
}

// TestMalformedProjectConfigIsReportedOnce is the same property for the
// project-local layer, which is read once per start too (issue #231).
func TestMalformedProjectConfigIsReportedOnce(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no user file, so only the project line can appear
	project := t.TempDir()
	t.Chdir(project)

	if err := os.MkdirAll(filepath.Join(project, ".tinycode"), 0755); err != nil {
		t.Fatal(err)
	}
	projectFile := filepath.Join(project, ".tinycode", "config.json")
	if err := os.WriteFile(projectFile, []byte(`{"providers": `), 0644); err != nil {
		t.Fatal(err)
	}

	var runErr error
	message := captureStderr(t, func() {
		cmd := newRootCmd()
		cmd.SetArgs([]string{"--list-sessions", "--session-dir", t.TempDir()})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		runErr = cmd.Execute()
	})
	if runErr != nil {
		t.Fatalf("a malformed project config must not fail the command: %v", runErr)
	}
	if got := strings.Count(message, "ignoring"); got != 1 {
		t.Errorf("the malformed project config was reported %d times, want 1: %s", got, message)
	}
	// The loader names the file the way it opened it: relative to the working
	// directory, which is the project root here.
	if named := filepath.Join(".tinycode", "config.json"); !strings.Contains(message, named) {
		t.Errorf("the diagnostic does not name %s: %s", named, message)
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
	reg, err := buildProviderRegistry(&config.Config{}, "", "", "")
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
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
	reg, err = buildProviderRegistry(cfg, "", "", "")
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
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

// TestBuildProviderRegistryWiresPromptCache covers the config-to-provider path
// (roadmap A4): a valid policy reaches the request, and a policy that cannot be
// honoured stops startup with the route named instead of being dropped.
func TestBuildProviderRegistryWiresPromptCache(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body = map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()

	cfg := &config.Config{Providers: []config.ProviderRecordConfig{{
		Name:        "cached",
		Type:        "openai",
		Model:       "m",
		BaseURL:     srv.URL,
		PromptCache: &config.PromptCacheConfig{Mode: "auto"},
	}}}
	reg, err := buildProviderRegistry(cfg, "sk-test", "", "")
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	if _, err := reg.Current().Chat(context.Background(), types.ChatRequest{
		Messages:  []types.Message{{Role: types.RoleSystem, Content: "sys"}},
		MaxTokens: 8,
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if key, ok := body["prompt_cache_key"].(string); !ok || key == "" {
		t.Errorf("prompt_cache_key = %v, want the configured policy to reach the request", body["prompt_cache_key"])
	}

	// A mode nobody can honour is an error naming the route, not a silent off.
	cfg.Providers[0].PromptCache = &config.PromptCacheConfig{Mode: "always"}
	if _, err := buildProviderRegistry(cfg, "", "", ""); err == nil ||
		!strings.Contains(err.Error(), `provider "cached"`) ||
		!strings.Contains(err.Error(), `unknown mode "always"`) {
		t.Errorf("invalid mode error = %v, want the route and the accepted values", err)
	}

	// A ttl without explicit breakpoints is contradictory for the same reason.
	cfg.Providers[0].PromptCache = &config.PromptCacheConfig{Mode: "auto", TTL: "1h"}
	if _, err := buildProviderRegistry(cfg, "", "", ""); err == nil ||
		!strings.Contains(err.Error(), `ttl "1h" needs mode "explicit"`) {
		t.Errorf("ttl with auto error = %v", err)
	}

	// The native Ollama backend accepts neither field.
	cfg.Providers = []config.ProviderRecordConfig{{
		Name: "local", Type: "ollama", Model: "llama",
		PromptCache: &config.PromptCacheConfig{Mode: "explicit"},
	}}
	if _, err := buildProviderRegistry(cfg, "", "", ""); err == nil ||
		!strings.Contains(err.Error(), `provider "local"`) ||
		!strings.Contains(err.Error(), "not supported") {
		t.Errorf("ollama error = %v, want a refusal naming the route", err)
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

// TestDefaultConfigSendsTheCanonicalDeepSeekModel is the acceptance check for
// issue #226. DeepSeek publishes `deepseek-flash` and footnotes
// `deepseek-v4-flash` as a retired alias: still accepted, served by
// DeepSeek-V4.1-Flash, billed at the same price. The alias is nevertheless a
// second name for one route, and the model name is also the key a declared rate
// is looked up by, so what this repository *sends* by default has to be the name
// the vendor documents.
//
// It asserts the request body rather than the config field: the field is what a
// provider may still override, the body is what the server bills on.
//
// The obvious companion assertion — that the shipped example declares a rate for
// the default route it ships — is deliberately not here yet: the example does
// not load at all, because a `$comment` inside the `agents` map is decoded as
// data (`json: cannot unmarshal string into Go struct field Config.agents.$comment
// of type config.AgentOverride`). Until that is fixed nothing in the file applies
// to a running program, so a gate over its contents would assert something no
// user ever sees. Filed as issue #229, which also records the two shapes that fix
// can take.
func TestDefaultConfigSendsTheCanonicalDeepSeekModel(t *testing.T) {
	var mu sync.Mutex
	var sent map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("request body is not JSON: %v (%s)", err, body)
		}
		mu.Lock()
		sent = decoded
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	cfg := config.DefaultConfig()
	registry, err := buildProviderRegistry(&cfg, "sk-test", "", srv.URL)
	if err != nil {
		t.Fatalf("buildProviderRegistry: %v", err)
	}
	// An empty request model is what the agent loop sends when no per-agent
	// override is configured, so this is the default path end to end.
	if _, err := registry.Current().Chat(context.Background(), types.ChatRequest{}); err != nil {
		t.Fatalf("Chat against the test server: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if sent == nil {
		t.Fatal("no request reached the test server")
	}
	if got := sent["model"]; got != "deepseek-flash" {
		t.Errorf(`request "model" = %v, want "deepseek-flash" (the published name; "deepseek-v4-flash" is a retired alias)`, got)
	}
}

// captureStdout is captureStderr for the informational commands, which print with
// fmt.Printf and therefore write to os.Stdout rather than to the cobra command's
// own writer.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = write
	defer func() { os.Stdout = previous }()
	fn()
	os.Stdout = previous
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	message, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	return string(message)
}

// TestUndoCommandRestoresTheLastToolCall covers the --undo path (issue #234, S2):
// one real tool call is recorded through the store, the flag returns the file to
// its pre-call bytes, and a second run says there is nothing left to undo without
// failing.
func TestUndoCommandRestoresTheLastToolCall(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	t.Chdir(project)
	restoreSandbox(t)
	tool.DefaultSandbox.ProjectRoot = project

	// The store root is process state that the first CLI run in this package sets
	// from the HOME it was given. This test writes a pre-image before any CLI run,
	// so it points the store at the root its own --undo will compute — asking
	// DefaultConfig rather than spelling the path, so a change to the default does
	// not leave this test asserting the wrong directory.
	previousRoot := tool.CheckpointRoot()
	tool.SetCheckpointRoot(config.DefaultConfig().CheckpointDir)
	t.Cleanup(func() { tool.SetCheckpointRoot(previousRoot) })

	target := filepath.Join(project, "target.txt")
	if err := os.WriteFile(target, []byte("before\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// The scope is what makes the write record a pre-image.
	scope := types.WithToolCallScope(context.Background(), types.ToolCallScope{
		Tool: "write_file", Session: "session-a", Run: 1, Step: 1, Call: 0,
	})
	if _, err := tool.WriteFile().Execute(scope, map[string]any{"path": target, "content": "after\n"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "after\n" {
		t.Fatalf("the write did not land: %q", got)
	}

	runUndo := func() string {
		t.Helper()
		cmd := newRootCmd()
		cmd.SetArgs([]string{"--undo"})
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		return captureStdout(t, func() {
			if err := cmd.Execute(); err != nil {
				t.Fatalf("--undo failed: %v", err)
			}
		})
	}

	out := runUndo()
	if got, _ := os.ReadFile(target); string(got) != "before\n" {
		t.Fatalf("after --undo the file is %q, want its pre-call bytes", got)
	}
	// The store records the path the fence handed the tool — the OS-resolved form,
	// which on macOS differs from the raw temp path (/var → /private/var).
	resolved := tool.DefaultSandbox.ResolvePath(target)
	if !strings.Contains(out, "Restored: "+resolved) {
		t.Errorf("--undo must name the file it restored, got %q", out)
	}

	// The call it just restored is out of reach, so a second run has nothing to
	// do — and that is not an error.
	out = runUndo()
	if !strings.Contains(out, "Nothing to undo") {
		t.Errorf("a second --undo must say there is nothing to undo, got %q", out)
	}
	if got, _ := os.ReadFile(target); string(got) != "before\n" {
		t.Errorf("the second --undo changed the file to %q", got)
	}
}
