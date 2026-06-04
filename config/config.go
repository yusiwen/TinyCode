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
)

// ProviderConfig holds LLM provider settings.
type ProviderConfig struct {
	Name   string `json:"name,omitempty"`
	Model  string `json:"model,omitempty"`
	BaseURL string `json:"base_url,omitempty"`
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

// Config is the top-level configuration structure.
type Config struct {
	DefaultMode  string                  `json:"default_mode,omitempty"`
	GlamourStyle string                  `json:"glamour_style,omitempty"`
	ShowThinking *bool                   `json:"show_thinking,omitempty"`
	Verbose      *bool                   `json:"verbose,omitempty"`
	Provider     ProviderConfig          `json:"provider,omitempty"`
	Truncation   TruncationConfig        `json:"truncation,omitempty"`
	Agents       map[string]AgentOverride `json:"agents,omitempty"`
	SessionDir   string                  `json:"session_dir,omitempty"`
	LSP          LSPConfig               `json:"lsp,omitempty"`
}

// LSPConfig holds LSP integration settings.
type LSPConfig struct {
	Enabled bool `json:"enabled,omitempty"`
}

// DefaultConfig returns the hardcoded default configuration.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	showThinking := true
	return Config{
		DefaultMode:  "plan",
		GlamourStyle: "auto",
		ShowThinking: &showThinking,
		Provider: ProviderConfig{
			Name:   "deepseek",
			BaseURL: "https://api.deepseek.com",
		},
		Truncation: TruncationConfig{
			MaxLines:  2000,
			MaxBytes:  200 * 1024,
			OutputDir: "/tmp/tinycode/truncated",
		},
		Agents: map[string]AgentOverride{
			"plan": {
				MaxSteps:    20,
				DeniedTools: []string{"write_file", "git_commit", "sandbox_allow", "task"},
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
// It returns a new Config without modifying dst or src.
func merge(dst, src Config) Config {
	if src.DefaultMode != "" {
		dst.DefaultMode = src.DefaultMode
	}
	if src.GlamourStyle != "" {
		dst.GlamourStyle = src.GlamourStyle
	}
	if src.ShowThinking != nil {
		dst.ShowThinking = src.ShowThinking
	}
	if src.Verbose != nil {
		dst.Verbose = src.Verbose
	}
	if src.Provider.Name != "" {
		dst.Provider.Name = src.Provider.Name
	}
	if src.Provider.Model != "" {
		dst.Provider.Model = src.Provider.Model
	}
	if src.Provider.BaseURL != "" {
		dst.Provider.BaseURL = src.Provider.BaseURL
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
	if src.SessionDir != "" {
		dst.SessionDir = src.SessionDir
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
