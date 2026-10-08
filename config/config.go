// Package config provides the unified configuration for TinyCode.
// Config files are loaded in order (later overrides earlier):
//  1. Code defaults (hardcoded)
//  2. ~/.tinycode/config.json (user global)
//  3. ./.tinycode/config.json (project local)
//  4. Environment variables / CLI flags (highest priority)
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ProviderRecordConfig holds one provider definition.
type ProviderRecordConfig struct {
	Name      string `json:"name,omitempty"`
	Type      string `json:"type,omitempty"` // "openai" or "ollama"
	Model     string `json:"model,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"` // env var name for API key
}

// TruncationConfig holds tool output truncation settings.
type TruncationConfig struct {
	MaxLines  int    `json:"max_lines,omitempty"`
	MaxBytes  int    `json:"max_bytes,omitempty"`
	OutputDir string `json:"output_dir,omitempty"`
}

// AgentRule mirrors agent.Rule for configuration files. Keeping a local copy
// avoids a config → agent dependency; main.go translates it.
type AgentRule struct {
	Action   string `json:"action"`             // tool name or "*"
	Resource string `json:"resource,omitempty"` // ignored today, defaults to "*"
	Effect   string `json:"effect"`             // "allow" | "deny"
}

// AgentOverride holds per-agent configuration overrides.
type AgentOverride struct {
	MaxSteps     int      `json:"max_steps,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	DeniedTools  []string `json:"denied_tools,omitempty"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
	Model        string   `json:"model,omitempty"` // "<provider>/<model>", e.g. "deepseek/deepseek-v4-pro"
	// Permissions replaces the agent's built-in ruleset when set (last match
	// wins, like the built-in rules). It takes precedence over
	// allowed_tools/denied_tools.
	Permissions []AgentRule `json:"permissions,omitempty"`
}

// APIKey returns the env var name to look up for this provider's API key.
// Priority: api_key_env (if set) → UPPER(NAME)_API_KEY
// Callers should fallback to OPENAI_API_KEY when this returns empty.
func (p ProviderRecordConfig) APIKey() string {
	if p.APIKeyEnv != "" {
		return p.APIKeyEnv
	}
	if p.Name != "" {
		return strings.ToUpper(p.Name) + "_API_KEY"
	}
	return "OPENAI_API_KEY"
}

type LSPConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// SandboxConfig holds tool sandbox configuration from config.json.
type SandboxConfig struct {
	ProjectRoot  string   `json:"project_root,omitempty"`
	DenyCommands []string `json:"deny_commands,omitempty"`
	AllowedPaths []string `json:"allowed_paths,omitempty"`

	// RequireHardBoundary refuses file operations that would be enforced by
	// in-process checks alone. It is for a deployment that must not run with a
	// weaker boundary than the kernel provides, and it fails closed: on a host
	// with no kernel mechanism every fenced operation is refused instead of
	// being silently allowed.
	RequireHardBoundary bool `json:"require_hard_boundary,omitempty"`

	// ConfineCommands runs shell commands under the kernel file boundary
	// instead of only under the string checks. Off by default, and worth
	// turning on only with the consequence understood: a confined command can
	// write only under the session's writable roots, so any toolchain that
	// writes its own cache outside them will fail.
	ConfineCommands bool `json:"confine_commands,omitempty"`
}

// MCPServerConfig defines a single MCP server to connect to.
type MCPServerConfig struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // "stdio" or "http"
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`     // for http transport
	Headers   map[string]string `json:"headers,omitempty"` // for http transport
}

// Config is the top-level configuration structure.
type Config struct {
	DefaultMode  string                   `json:"default_mode,omitempty"`
	ShowThinking *bool                    `json:"show_thinking,omitempty"`
	Verbose      *bool                    `json:"verbose,omitempty"`
	Providers    []ProviderRecordConfig   `json:"providers,omitempty"`
	Truncation   *TruncationConfig        `json:"truncation,omitempty"`
	Agents       map[string]AgentOverride `json:"agents,omitempty"`
	Sandbox      *SandboxConfig           `json:"sandbox,omitempty"`
	Theme        string                   `json:"theme,omitempty"`
	SessionDir   string                   `json:"session_dir,omitempty"`
	LSP          *LSPConfig               `json:"lsp,omitempty"`
	LogLevel     string                   `json:"log_level,omitempty"`

	// Context window and compression
	ContextLength        int `json:"context_length,omitempty"`
	CompressionThreshold int `json:"compression_threshold,omitempty"`

	// Web search backends
	SearXNGURL string `json:"searxng_url,omitempty"`

	// MCP servers
	MCPServers []MCPServerConfig `json:"mcp_servers,omitempty"`
}

// DefaultConfig returns the hardcoded default configuration.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	showThinking := true
	return Config{
		DefaultMode:  "plan",
		ShowThinking: &showThinking,
		Providers: []ProviderRecordConfig{
			{
				Name:    "deepseek",
				Type:    "openai",
				Model:   "deepseek-v4-flash",
				BaseURL: "https://api.deepseek.com",
			},
		},
		Truncation: &TruncationConfig{
			MaxLines:  2000,
			MaxBytes:  200 * 1024,
			OutputDir: "/tmp/tinycode/truncated",
		},
		Agents: map[string]AgentOverride{
			"plan": {
				MaxSteps:    20,
				DeniedTools: []string{"write_file", "git_commit", "sandbox_allow", "task", "skill_manage"},
			},
			"explore": {
				// Read-only sub-agent: keep the built-in read_file/search_files
				// permission ruleset (see agent.DefaultAgents) instead of
				// granting bash here.
				MaxSteps: 15,
			},
		},
		SessionDir:           filepath.Join(home, ".tinycode", "sessions"),
		ContextLength:        1000000, // 1M for DeepSeek V4 Flash
		CompressionThreshold: 500000,  // 50% of context
	}
}

// loadFile reads and parses a JSON config file, returning the partial config.
func loadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		// A malformed config must not silently fall back to the defaults: say so
		// on stderr so the user can fix it.
		fmt.Fprintf(os.Stderr, "tinycode: ignoring %s: %v\n", path, err)
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// merge applies overrides from src into dst (non-zero fields override).
func merge(dst, src Config) Config {
	if src.DefaultMode != "" {
		dst.DefaultMode = src.DefaultMode
	}
	if src.ShowThinking != nil {
		dst.ShowThinking = src.ShowThinking
	}
	if src.Verbose != nil {
		dst.Verbose = src.Verbose
	}
	if len(src.Providers) > 0 {
		dst.Providers = src.Providers
	}
	if src.Truncation != nil {
		if dst.Truncation == nil {
			dst.Truncation = &TruncationConfig{}
		}
		if src.Truncation.MaxLines > 0 {
			dst.Truncation.MaxLines = src.Truncation.MaxLines
		}
		if src.Truncation.MaxBytes > 0 {
			dst.Truncation.MaxBytes = src.Truncation.MaxBytes
		}
		if src.Truncation.OutputDir != "" {
			dst.Truncation.OutputDir = src.Truncation.OutputDir
		}
	}
	if src.SessionDir != "" {
		dst.SessionDir = src.SessionDir
	}
	if src.LogLevel != "" {
		dst.LogLevel = src.LogLevel
	}
	if src.ContextLength > 0 {
		dst.ContextLength = src.ContextLength
	}
	if src.CompressionThreshold > 0 {
		dst.CompressionThreshold = src.CompressionThreshold
	}
	if src.LSP != nil {
		if dst.LSP == nil {
			dst.LSP = &LSPConfig{}
		}
		if src.LSP.Enabled {
			dst.LSP.Enabled = true
		}
	}
	if src.Theme != "" {
		dst.Theme = src.Theme
	}
	if src.SearXNGURL != "" {
		dst.SearXNGURL = src.SearXNGURL
	}
	if len(src.MCPServers) > 0 {
		dst.MCPServers = src.MCPServers
	}

	// Merge sandbox hardening settings field by field so a project config can
	// add deny rules without dropping the user-level ones.
	if src.Sandbox != nil {
		if dst.Sandbox == nil {
			dst.Sandbox = &SandboxConfig{}
		}
		if src.Sandbox.ProjectRoot != "" {
			dst.Sandbox.ProjectRoot = src.Sandbox.ProjectRoot
		}
		if len(src.Sandbox.DenyCommands) > 0 {
			dst.Sandbox.DenyCommands = append(dst.Sandbox.DenyCommands, src.Sandbox.DenyCommands...)
		}
		if len(src.Sandbox.AllowedPaths) > 0 {
			dst.Sandbox.AllowedPaths = append(dst.Sandbox.AllowedPaths, src.Sandbox.AllowedPaths...)
		}
		// A bool has no "unset" to distinguish from false, so it is only
		// inherited when the overlay turns it on: a project file cannot turn
		// off a user-level requirement, and a user file cannot turn it on by
		// being absent.
		if src.Sandbox.RequireHardBoundary {
			dst.Sandbox.RequireHardBoundary = true
		}
		if src.Sandbox.ConfineCommands {
			dst.Sandbox.ConfineCommands = true
		}
	}

	// Merge agent overrides
	if dst.Agents == nil {
		dst.Agents = make(map[string]AgentOverride)
	}
	for name, override := range src.Agents {
		existing, has := dst.Agents[name]
		if !has {
			dst.Agents[name] = override
			continue
		}
		if override.MaxSteps > 0 {
			existing.MaxSteps = override.MaxSteps
		}
		if override.SystemPrompt != "" {
			existing.SystemPrompt = override.SystemPrompt
		}
		if override.Model != "" {
			existing.Model = override.Model
		}
		if override.Permissions != nil {
			existing.Permissions = override.Permissions
		}
		if override.AllowedTools != nil {
			existing.AllowedTools = override.AllowedTools
		}
		if override.DeniedTools != nil {
			existing.DeniedTools = override.DeniedTools
		}
		dst.Agents[name] = existing
	}

	return dst
}

// LoadUserConfig loads only the code defaults and the user-global config file,
// skipping the project-local layer. Use it when persisting settings back to
// disk: a repository's ./.tinycode/config.json is attacker-controlled, so its
// providers or system prompts must never be promoted into the user's global
// config by an unrelated action such as "Always allow".
func LoadUserConfig() Config {
	cfg := DefaultConfig()

	home, err := os.UserHomeDir()
	if err == nil {
		userCfg, err := loadFile(filepath.Join(home, ".tinycode", "config.json"))
		if err == nil {
			cfg = merge(cfg, userCfg)
		}
	}

	return cfg
}

// LoadConfig loads the configuration from all sources and returns the merged result.
// Load order: defaults → ~/.tinycode/config.json → ./.tinycode/config.json
func LoadConfig() Config {
	cfg := LoadUserConfig()

	projCfg, err := loadFile(filepath.Join(".tinycode", "config.json"))
	if err == nil {
		cfg = merge(cfg, projCfg)
	}

	return cfg
}

// Save persists the configuration to the user's global config file.
func (cfg Config) Save() error {
	path, err := userConfigPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// AddAllowedPath appends path to sandbox.allowed_paths in the *user* config
// file, preserving every other key in that file (including keys this build does
// not know about). It deliberately does not re-serialise a merged Config, which
// would freeze today's defaults into the user's file and could leak project-local
// settings.
func AddAllowedPath(path string) error {
	if path == "" {
		return fmt.Errorf("refusing to allow an empty path")
	}
	file, err := userConfigPath()
	if err != nil {
		return err
	}

	// Start from the raw file so unknown keys survive the round trip. A missing
	// or malformed file starts from an empty object.
	raw := map[string]any{}
	if data, readErr := os.ReadFile(file); readErr == nil {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse %s: %w", file, err)
		}
	}

	sandbox, _ := raw["sandbox"].(map[string]any)
	if sandbox == nil {
		sandbox = map[string]any{}
	}
	existing, _ := sandbox["allowed_paths"].([]any)
	for _, p := range existing {
		if s, ok := p.(string); ok && s == path {
			return nil // already allowed
		}
	}
	sandbox["allowed_paths"] = append(existing, path)
	raw["sandbox"] = sandbox

	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(file, data)
}

// userConfigPath returns ~/.tinycode/config.json, creating the directory.
func userConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".tinycode", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("create config dir: %w", err)
	}
	return path, nil
}

// UserConfigPath is the file a persistent grant is written to, for callers that
// must name it: a person choosing "always allow" is changing a file, and the
// choice is only deliberate if the file is visible when it is made.
func UserConfigPath() (string, error) { return userConfigPath() }

// AllowedPathGrant is one persistent permission grant: a path a person allowed
// beyond the current session, with the context needed to audit it later.
type AllowedPathGrant struct {
	Path string `json:"path"`
	// Granted is when the grant was recorded. It is zero for a grant read from
	// the older `allowed_paths` list, which carried no context.
	Granted time.Time `json:"granted,omitempty"`
	// Project is the project root in effect when the grant was made, so a
	// grant that no longer fits where it came from can be spotted.
	Project string `json:"project,omitempty"`
	// Legacy marks a grant that predates the record format.
	Legacy bool `json:"-"`
}

// ListAllowedPathGrants returns every persistent grant, newest format and
// legacy list alike, sorted by path. A path present in both forms appears once,
// described by its record.
func ListAllowedPathGrants() ([]AllowedPathGrant, error) {
	file, err := userConfigPath()
	if err != nil {
		return nil, err
	}
	raw, err := readConfigObject(file)
	if err != nil {
		return nil, err
	}
	return grantsFromRaw(raw), nil
}

// AddAllowedPathGrant records a persistent grant for path, preserving every
// other key in the user's file (including keys this build does not know about).
// A path already granted in either form is left alone rather than duplicated,
// so the recorded time and project describe when the grant was actually made.
func AddAllowedPathGrant(path, project string) error {
	if path == "" {
		return fmt.Errorf("refusing to allow an empty path")
	}
	file, err := userConfigPath()
	if err != nil {
		return err
	}
	raw, err := readConfigObject(file)
	if err != nil {
		return err
	}
	for _, grant := range grantsFromRaw(raw) {
		if grant.Path == path {
			return nil // already granted
		}
	}

	sandbox := sandboxObject(raw)
	records, _ := sandbox["allowed_path_grants"].([]any)
	sandbox["allowed_path_grants"] = append(records, map[string]any{
		"path":    path,
		"granted": time.Now().UTC().Format(time.RFC3339),
		"project": project,
	})
	raw["sandbox"] = sandbox

	return writeConfigObject(file, raw)
}

// RevokeAllowedPathGrant removes path from both the record list and the legacy
// list, and reports whether anything was removed. Revoking is the only way a
// persistent grant ends, so it has to be able to reach every form it can be
// stored in.
func RevokeAllowedPathGrant(path string) (bool, error) {
	if path == "" {
		return false, fmt.Errorf("refusing to revoke an empty path")
	}
	file, err := userConfigPath()
	if err != nil {
		return false, err
	}
	raw, err := readConfigObject(file)
	if err != nil {
		return false, err
	}
	sandbox := sandboxObject(raw)

	removed := false
	if records, ok := sandbox["allowed_path_grants"].([]any); ok {
		kept := make([]any, 0, len(records))
		for _, record := range records {
			if m, ok := record.(map[string]any); ok && m["path"] == path {
				removed = true
				continue
			}
			kept = append(kept, record)
		}
		if len(kept) == 0 {
			delete(sandbox, "allowed_path_grants")
		} else {
			sandbox["allowed_path_grants"] = kept
		}
	}
	if legacy, ok := sandbox["allowed_paths"].([]any); ok {
		kept := make([]any, 0, len(legacy))
		for _, entry := range legacy {
			if s, ok := entry.(string); ok && s == path {
				removed = true
				continue
			}
			kept = append(kept, entry)
		}
		if len(kept) == 0 {
			delete(sandbox, "allowed_paths")
		} else {
			sandbox["allowed_paths"] = kept
		}
	}
	if !removed {
		return false, nil
	}
	raw["sandbox"] = sandbox
	if err := writeConfigObject(file, raw); err != nil {
		return false, err
	}
	return true, nil
}

// readConfigObject reads the raw user config as a generic object so unknown
// keys survive a round trip. A missing file is an empty object; a malformed one
// is an error rather than a silent overwrite.
func readConfigObject(file string) (map[string]any, error) {
	raw := map[string]any{}
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return raw, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	return raw, nil
}

func writeConfigObject(file string, raw map[string]any) error {
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(file, data)
}

func sandboxObject(raw map[string]any) map[string]any {
	if sandbox, ok := raw["sandbox"].(map[string]any); ok {
		return sandbox
	}
	return map[string]any{}
}

// grantsFromRaw reads both storage forms. Records win over the legacy list for
// the same path, because a record is what a grant looks like once it has
// context.
func grantsFromRaw(raw map[string]any) []AllowedPathGrant {
	sandbox := sandboxObject(raw)
	if len(sandbox) == 0 {
		return nil
	}

	byPath := map[string]AllowedPathGrant{}
	if legacy, ok := sandbox["allowed_paths"].([]any); ok {
		for _, entry := range legacy {
			if s, ok := entry.(string); ok && s != "" {
				byPath[s] = AllowedPathGrant{Path: s, Legacy: true}
			}
		}
	}
	if records, ok := sandbox["allowed_path_grants"].([]any); ok {
		for _, record := range records {
			m, ok := record.(map[string]any)
			if !ok {
				continue
			}
			path, _ := m["path"].(string)
			if path == "" {
				continue
			}
			grant := AllowedPathGrant{Path: path}
			grant.Project, _ = m["project"].(string)
			if s, ok := m["granted"].(string); ok {
				if when, err := time.Parse(time.RFC3339, s); err == nil {
					grant.Granted = when
				}
			}
			byPath[path] = grant
		}
	}

	grants := make([]AllowedPathGrant, 0, len(byPath))
	for _, grant := range byPath {
		grants = append(grants, grant)
	}
	sort.Slice(grants, func(i, j int) bool { return grants[i].Path < grants[j].Path })
	return grants
}

// writeFileAtomic writes data to path through a temp file in the same directory
// so a crash mid-write cannot truncate the file. The file is created 0600
// because it can contain API endpoints and other local preferences.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
