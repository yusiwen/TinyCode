package main

// A/B variants (roadmap F2): two prompt variants, two skill sets or two models run
// over one task set and compared, instead of the three configurations baked into
// the runner.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// variantSpec is one entry of a -variants file.
type variantSpec struct {
	Name string `json:"name"`
	// Mode is the agent the attempt runs as: build (writes) or plan (read-only).
	Mode string `json:"mode,omitempty"`
	// SystemPrompt replaces the agent's prompt; SystemPromptFile reads it from a
	// file, so a prompt can live next to the experiment rather than in the JSON.
	SystemPrompt     string `json:"system_prompt,omitempty"`
	SystemPromptFile string `json:"system_prompt_file,omitempty"`
	// Model is "<provider>/<model>", the same form the config's agent override uses.
	Model string `json:"model,omitempty"`
	// MaxSteps overrides the agent's step budget for this variant.
	MaxSteps int `json:"max_steps,omitempty"`
	// MaxTokensPerRun is the hard budget for one attempt; 0 leaves the default.
	MaxTokensPerRun int `json:"max_tokens_per_run,omitempty"`
	// RepoMap prepends a symbol digest of the work copy, the treatment arm of the
	// repository-index spike (roadmap B4).
	RepoMap bool `json:"repo_map,omitempty"`
}

// loadVariants reads a -variants file into configurations, validating what a
// comparison depends on: one name per variant, a mode the runner knows, and a
// prompt that exists if one is named.
func loadVariants(path string) ([]configuration, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var specs []variantSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(specs) < 2 {
		return nil, fmt.Errorf("%s defines %d variant(s); an A/B comparison needs at least two", path, len(specs))
	}
	seen := make(map[string]bool, len(specs))
	configs := make([]configuration, 0, len(specs))
	for index, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			return nil, fmt.Errorf("%s: variant %d has no name", path, index)
		}
		if seen[name] {
			return nil, fmt.Errorf("%s: two variants are named %q", path, name)
		}
		seen[name] = true
		mode := spec.Mode
		if mode == "" {
			mode = "build"
		}
		if mode != "build" && mode != "plan" {
			return nil, fmt.Errorf("%s: variant %q has mode %q; use build or plan", path, name, mode)
		}
		prompt := spec.SystemPrompt
		if spec.SystemPromptFile != "" {
			if prompt != "" {
				return nil, fmt.Errorf("%s: variant %q sets both system_prompt and system_prompt_file", path, name)
			}
			data, err := os.ReadFile(spec.SystemPromptFile)
			if err != nil {
				return nil, fmt.Errorf("%s: variant %q: %w", path, name, err)
			}
			prompt = string(data)
		}
		configs = append(configs, configuration{
			Name:            name,
			Mode:            mode,
			SystemPrompt:    prompt,
			Model:           spec.Model,
			MaxSteps:        spec.MaxSteps,
			MaxTokensPerRun: spec.MaxTokensPerRun,
			RepoMap:         spec.RepoMap,
		})
	}
	return configs, nil
}
