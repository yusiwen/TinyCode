package config

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shippedExample is the template the repository tells the reader to copy.
const shippedExample = "../config.example.json"

// TestShippedExampleLoads is the regression guard for issue #229. The file whose
// first line says "Copy to config.json" has to survive that copy, and it did not:
// its "$comment" inside the agents map was decoded as an entry, so loadFile
// reported an error and both callers merged nothing. A user who followed the
// instruction lost every section — providers, pricing, sandbox, notify, budget —
// and got the built-in defaults with one line on stderr.
func TestShippedExampleLoads(t *testing.T) {
	// Read the template before moving the process, since the path is relative to
	// the package directory.
	data, err := os.ReadFile(shippedExample)
	if err != nil {
		t.Fatalf("read %s: %v", shippedExample, err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir()) // no project-local ./.tinycode/config.json in the way

	dir := filepath.Join(home, ".tinycode")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	cfg := loadConfigWithSilentStderr(t)

	// Values the example sets and DefaultConfig does not, so a silent fallback to
	// the defaults cannot pass for a successful load.
	if len(cfg.Providers) != 5 {
		t.Errorf("providers = %d, want the 5 the example lists (update this count if the example gains or loses one)", len(cfg.Providers))
	}
	if cfg.Theme != "nord" {
		t.Errorf("theme = %q, want nord", cfg.Theme)
	}
	if cfg.Pricing == nil || len(cfg.Pricing.Prices) != 2 {
		t.Fatalf("pricing = %+v, want the two routes the example declares", cfg.Pricing)
	}
	if _, ok := cfg.Pricing.Prices["deepseek/deepseek-flash"]; !ok {
		t.Errorf("declared rates = %v, want the route the example ships as its own default", cfg.Pricing.Prices)
	}
	if len(cfg.MCPServers) != 2 {
		t.Errorf("mcp_servers = %d, want the 2 the example lists", len(cfg.MCPServers))
	}
	if cfg.Sandbox == nil {
		t.Fatal("sandbox is nil, want the example's section")
	}
	// An angle-bracket placeholder proves the walk preserved the value instead of
	// re-encoding it: json.Marshal escapes "<" and ">", and a decode of a
	// double-escaped document would not give this string back.
	if cfg.Sandbox.ProjectRoot != "<PROJECT_ROOT>" {
		t.Errorf("sandbox.project_root = %q, want <PROJECT_ROOT>", cfg.Sandbox.ProjectRoot)
	}
}

// loadConfigWithSilentStderr runs LoadConfig with os.Stderr redirected and fails
// the test if the loader complained. A parse failure is reported on stderr and
// then absorbed — the callers merge nothing — so the values alone cannot say
// whether the file was read or discarded.
func loadConfigWithSilentStderr(t *testing.T) Config {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = write
	cfg := LoadConfig()
	os.Stderr = previous
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	message, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if len(message) > 0 {
		t.Errorf("loading the shipped example wrote to stderr: %s", message)
	}
	return cfg
}

// TestDollarKeysAreDocumentationAtEveryDepth pins the rule the loader now
// follows: a key beginning with "$" is a note, not data, wherever it sits. The
// map-shaped sections are the ones it matters in — agents, pricing.prices and an
// MCP server's env and headers all read a "$comment" as an entry if it reaches
// them — and one such note used to cost the entire file (issue #229).
func TestDollarKeysAreDocumentationAtEveryDepth(t *testing.T) {
	path := writeDocument(t, `{
	  "$comment": "top level",
	  "$new_fields": {"any": "blob", "$nested": "a note inside a note"},
	  "theme": "nord",
	  "providers": [
	    {"name": "deepseek", "type": "openai", "model": "deepseek-flash"}
	  ],
	  "agents": {
	    "$comment": "the position that used to break the file",
	    "build": {"max_steps": 7}
	  },
	  "pricing": {
	    "currency": "USD",
	    "prices": {
	      "$comment": "next to the rates",
	      "deepseek/*": {"input_per_million": 0.3}
	    }
	  },
	  "sandbox": {
	    "$comment": "a struct position, which must keep working too",
	    "project_root": "/srv/app",
	    "allowed_paths": ["/srv/app/data"]
	  },
	  "mcp_servers": [
	    {"name": "demo", "transport": "stdio", "command": "/bin/echo",
	     "env": {"$comment": "a map inside an array", "LOG": "debug"}},
	    {"name": "remote", "transport": "http", "url": "http://localhost:9000/mcp",
	     "headers": {"$comment": "another map inside an array", "Authorization": "Bearer token"}}
	  ]
	}`)

	cfg, err := loadFile(path)
	if err != nil {
		t.Fatalf("loadFile: %v", err)
	}

	if cfg.Theme != "nord" {
		t.Errorf("theme = %q, want nord", cfg.Theme)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(cfg.Providers))
	}
	if len(cfg.Agents) != 1 || cfg.Agents["build"].MaxSteps != 7 {
		t.Errorf("agents = %+v, want only the build override", cfg.Agents)
	}
	if cfg.Pricing == nil || len(cfg.Pricing.Prices) != 1 {
		t.Fatalf("prices = %+v, want only the declared rate", cfg.Pricing)
	}
	if cfg.Pricing.Currency != "USD" {
		t.Errorf("pricing.currency = %q, want USD", cfg.Pricing.Currency)
	}
	if _, ok := cfg.Pricing.Prices["deepseek/*"]; !ok {
		t.Errorf("prices = %v, want the deepseek/* rate", cfg.Pricing.Prices)
	}
	if cfg.Sandbox == nil || cfg.Sandbox.ProjectRoot != "/srv/app" || len(cfg.Sandbox.AllowedPaths) != 1 {
		t.Errorf("sandbox = %+v, want the declared project root and allowed path", cfg.Sandbox)
	}
	if len(cfg.MCPServers) != 2 {
		t.Fatalf("mcp_servers = %d, want 2", len(cfg.MCPServers))
	}
	if got := cfg.MCPServers[0].Env; len(got) != 1 || got["LOG"] != "debug" {
		t.Errorf("mcp server env = %v, want only LOG=debug", got)
	}
	if got := cfg.MCPServers[1].Headers; len(got) != 1 || got["Authorization"] != "Bearer token" {
		t.Errorf("mcp server headers = %v, want only Authorization", got)
	}
}

// TestStripDocumentationKeysIsRecursive walks the helper on a document deeper
// than any section, so the recursion is pinned independently of which sections
// happen to decode into a map today.
func TestStripDocumentationKeysIsRecursive(t *testing.T) {
	const document = `{
	  "keep": 1,
	  "$drop": "top",
	  "object": {
	    "$drop": "nested",
	    "keep": {
	      "$drop": "deeper",
	      "items": [{"$drop": "inside an array", "keep": 2}]
	    }
	  }
	}`

	cleaned, err := stripDocumentationKeys([]byte(document))
	if err != nil {
		t.Fatalf("stripDocumentationKeys: %v", err)
	}
	if strings.Contains(string(cleaned), "$") {
		t.Errorf("a $ key survived the walk: %s", cleaned)
	}

	// The data around the notes has to be exactly what it was.
	var got struct {
		Keep   int `json:"keep"`
		Object struct {
			Keep struct {
				Items []struct {
					Keep int `json:"keep"`
				} `json:"items"`
			} `json:"keep"`
		} `json:"object"`
	}
	if err := json.Unmarshal(cleaned, &got); err != nil {
		t.Fatalf("the stripped document is not JSON: %v (%s)", err, cleaned)
	}
	if got.Keep != 1 || len(got.Object.Keep.Items) != 1 || got.Object.Keep.Items[0].Keep != 2 {
		t.Errorf("stripped document = %+v, want the values the notes sat between", got)
	}
}

// TestAllowedPathRoundTripKeepsDollarComments guards the other half of the rule.
// "Always allow" rewrites the user's file from its raw JSON, so a note has to
// survive that write and the file has to keep loading afterwards: dropping the
// notes would be the easy way to make the load succeed, and it would delete the
// user's own documentation (issue #229).
func TestAllowedPathRoundTripKeepsDollarComments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".tinycode")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{
  "$comment": "top",
  "agents": {"$comment": "the note that used to break the file", "build": {"max_steps": 7}},
  "sandbox": {"$comment": "raw round trip", "allowed_paths": ["/already/there"]}
}`), 0600); err != nil {
		t.Fatal(err)
	}

	if err := AddAllowedPath("/new/path"); err != nil {
		t.Fatalf("AddAllowedPath: %v", err)
	}

	patched, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(patched), `"$comment"`); count != 3 {
		t.Errorf("the rewritten file carries %d of the 3 $comment keys:\n%s", count, patched)
	}

	cfg, err := loadFile(path)
	if err != nil {
		t.Fatalf("the rewritten file does not load: %v", err)
	}
	if cfg.Agents["build"].MaxSteps != 7 {
		t.Errorf("agents = %+v, want the build override", cfg.Agents)
	}
	if cfg.Sandbox == nil || len(cfg.Sandbox.AllowedPaths) != 2 {
		t.Errorf("sandbox = %+v, want both allowed paths", cfg.Sandbox)
	}
}

// writeDocument writes one config document to a temp file and returns its path.
func writeDocument(t *testing.T, document string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(document), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
