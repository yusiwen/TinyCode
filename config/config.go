// Package config provides the unified configuration for TinyCode.
// Config files are loaded in order (later overrides earlier):
//   1. Code defaults (hardcoded)
//   2. ~/.tinycode/config.json (user global)
//   3. ./.tinycode/config.json (project local)
//   4. Environment variables / CLI flags (highest priority)
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ProviderRecordConfig holds one provider definition.
type ProviderRecordConfig struct {
	Name      string `json:"name,omitempty"`
	Type      string `json:"type,omitempty"`       // "openai" or "ollama"
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

// AgentOverride holds per-agent configuration overrides.
type AgentOverride struct {
	MaxSteps     int      `json:"max_steps,omitempty"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	DeniedTools  []string `json:"denied_tools,omitempty"`
	SystemPrompt string   `json:"system_prompt,omitempty"`
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
	ProjectRoot      string   `json:"project_root,omitempty"`
	DenyCommands     []string `json:"deny_commands,omitempty"`
}

// Config is the top-level configuration structure.
type Config struct {
	DefaultMode  string                   `json:"default_mode,omitempty"`
	ShowThinking *bool                    `json:"show_thinking,omitempty"`
	Verbose      *bool                    `json:"verbose,omitempty"`
	Providers    []ProviderRecordConfig   `json:"providers,omitempty"`
	Truncation   *TruncationConfig         `json:"truncation,omitempty"`
	Agents       map[string]AgentOverride `json:"agents,omitempty"`
	Sandbox      *SandboxConfig            `json:"sandbox,omitempty"`
	Theme        string                   `json:"theme,omitempty"`
	SessionDir   string                   `json:"session_dir,omitempty"`
	LSP          *LSPConfig               `json:"lsp,omitempty"`
	LogLevel     string                  `json:"log_level,omitempty"`

	// Context window and compression
	ContextLength        int `json:"context_length,omitempty"`
	CompressionThreshold int `json:"compression_threshold,omitempty"`

	// Web search backends
	SearXNGURL string `json:"searxng_url,omitempty"`
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
			"build": {
				MaxSteps: 30,
			},
			"explore": {
				MaxSteps:     15,
				AllowedTools: []string{"bash", "read_file", "search_files"},
			},
		},
		SessionDir: filepath.Join(home, ".tinycode", "sessions"),
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

// LoadConfig loads the configuration from all sources and returns the merged result.
// Load order: defaults → ~/.tinycode/config.json → ./.tinycode/config.json
func LoadConfig() Config {
	cfg := DefaultConfig()

	home, err := os.UserHomeDir()
	if err == nil {
		userCfg, err := loadFile(filepath.Join(home, ".tinycode", "config.json"))
		if err == nil {
			cfg = merge(cfg, userCfg)
		}
	}

	projCfg, err := loadFile(filepath.Join(".tinycode", "config.json"))
	if err == nil {
		cfg = merge(cfg, projCfg)
	}

	return cfg
}

// Save persists the configuration to the user's global config file.
func (cfg Config) Save() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".tinycode", "config.json")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}
