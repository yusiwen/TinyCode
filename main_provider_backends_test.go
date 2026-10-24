package main

import (
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
)

// TestBuildProviderRegistryNativeBackends: each documented type builds the
// provider it names, and carries the route identity pricing and failover use.
func TestBuildProviderRegistryNativeBackends(t *testing.T) {
	cases := []struct {
		name      string
		pc        config.ProviderRecordConfig
		wantName  string
		wantModel string
	}{
		{
			name:      "anthropic",
			pc:        config.ProviderRecordConfig{Name: "claude", Type: "anthropic", Model: "claude-sonnet-4"},
			wantName:  "anthropic/claude-sonnet-4",
			wantModel: "claude-sonnet-4",
		},
		{
			name:      "gemini",
			pc:        config.ProviderRecordConfig{Name: "gem", Type: "gemini", Model: "gemini-2.5-pro"},
			wantName:  "gemini/gemini-2.5-pro",
			wantModel: "gemini-2.5-pro",
		},
		{
			name: "bedrock",
			pc: config.ProviderRecordConfig{
				Name: "aws", Type: "bedrock", Model: "anthropic.claude-sonnet-4",
				Region: "us-east-1", BaseURL: "https://bedrock.invalid",
			},
			wantName:  "bedrock/anthropic.claude-sonnet-4",
			wantModel: "anthropic.claude-sonnet-4",
		},
		{
			name: "azure",
			pc: config.ProviderRecordConfig{
				Name: "az", Type: "azure", Model: "my-deployment",
				BaseURL: "https://example.openai.azure.com",
			},
			wantName:  "azure/my-deployment",
			wantModel: "my-deployment",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg, err := buildProviderRegistry(&config.Config{Providers: []config.ProviderRecordConfig{tc.pc}}, "", "", "")
			if err != nil {
				t.Fatalf("buildProviderRegistry: %v", err)
			}
			cur := reg.Current()
			if got := cur.Name(); got != tc.wantName {
				t.Errorf("Name = %q, want %q", got, tc.wantName)
			}
			info, ok := cur.(agent.RouteInfo)
			if !ok {
				t.Fatalf("%T does not implement RouteInfo", cur)
			}
			if info.Route() != tc.pc.Name {
				t.Errorf("Route = %q, want %q", info.Route(), tc.pc.Name)
			}
			if info.DefaultModel() != tc.wantModel {
				t.Errorf("DefaultModel = %q, want %q", info.DefaultModel(), tc.wantModel)
			}
		})
	}
}

// TestBuildProviderRegistryRefusesUnknownType: a typo in type must not silently
// send a vendor's key to the OpenAI-compatible endpoint.
func TestBuildProviderRegistryRefusesUnknownType(t *testing.T) {
	_, err := buildProviderRegistry(&config.Config{Providers: []config.ProviderRecordConfig{{
		Name: "typo", Type: "antropic", Model: "m",
	}}}, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "unknown type") {
		t.Fatalf("error = %v, want an unknown-type error", err)
	}
}

// TestValidateBackendOptions pins which backend supports which cache mode and
// whether it can take a thinking budget.
func TestValidateBackendOptions(t *testing.T) {
	explicit := agent.PromptCachePolicy{Mode: agent.PromptCacheExplicit}
	explicit1h := agent.PromptCachePolicy{Mode: agent.PromptCacheExplicit, TTL: "1h"}
	auto := agent.PromptCachePolicy{Mode: agent.PromptCacheAuto}
	off := agent.PromptCachePolicy{Mode: agent.PromptCacheOff}

	cases := []struct {
		name    string
		typ     string
		policy  agent.PromptCachePolicy
		pc      config.ProviderRecordConfig
		wantErr string
	}{
		{"openai explicit", "openai", explicit, config.ProviderRecordConfig{}, ""},
		{"anthropic explicit", "anthropic", explicit, config.ProviderRecordConfig{}, ""},
		{"anthropic auto", "anthropic", auto, config.ProviderRecordConfig{}, "auto"},
		{"gemini auto", "gemini", auto, config.ProviderRecordConfig{}, ""},
		{"gemini explicit", "gemini", explicit, config.ProviderRecordConfig{}, "explicit"},
		{"bedrock explicit", "bedrock", explicit, config.ProviderRecordConfig{}, ""},
		{"bedrock auto", "bedrock", auto, config.ProviderRecordConfig{}, "auto"},
		{"bedrock 1h", "bedrock", explicit1h, config.ProviderRecordConfig{}, "ttl"},
		{"azure off", "azure", off, config.ProviderRecordConfig{}, ""},
		{"azure auto", "azure", auto, config.ProviderRecordConfig{}, "not supported"},
		{"ollama auto", "ollama", auto, config.ProviderRecordConfig{}, "not supported"},
		{"openai thinking", "openai", off, config.ProviderRecordConfig{ThinkingBudgetTokens: 10}, "thinking_budget_tokens"},
		{"anthropic thinking", "anthropic", off, config.ProviderRecordConfig{ThinkingBudgetTokens: 10}, ""},
		{"negative thinking", "anthropic", off, config.ProviderRecordConfig{ThinkingBudgetTokens: -1}, "negative"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBackendOptions(tc.typ, "route", tc.policy, tc.pc)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestAzureRequiresResourceEndpoint: Azure cannot guess a resource name, so an
// empty base URL is a startup error rather than a request to somewhere wrong.
func TestAzureRequiresResourceEndpoint(t *testing.T) {
	_, err := buildProviderRegistry(&config.Config{Providers: []config.ProviderRecordConfig{{
		Name: "az", Type: "azure", Model: "deploy",
	}}}, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Fatalf("error = %v, want a base_url error", err)
	}
}

// TestProviderKeyFallbacks: a native route falls back to its own vendor variable,
// never to OPENAI_API_KEY, and an explicit api_key_env still wins.
func TestProviderKeyFallbacks(t *testing.T) {
	env := map[string]string{
		"ANTHROPIC_API_KEY":    "ant-key",
		"GEMINI_API_KEY":       "gem-key",
		"GOOGLE_API_KEY":       "google-key",
		"AZURE_OPENAI_API_KEY": "azure-key",
		"OPENAI_API_KEY":       "openai-key",
		"MY_OWN_VARIABLE":      "own-key",
	}
	get := func(k string) string { return env[k] }

	cases := []struct {
		name string
		pc   config.ProviderRecordConfig
		want string
	}{
		{"anthropic named", config.ProviderRecordConfig{Name: "claude", Type: "anthropic"}, "ant-key"},
		{"gemini named", config.ProviderRecordConfig{Name: "gem", Type: "gemini"}, "gem-key"},
		{"azure named", config.ProviderRecordConfig{Name: "az", Type: "azure"}, "azure-key"},
		{"openai falls back to openai", config.ProviderRecordConfig{Name: "ds", Type: "openai"}, "openai-key"},
		{"explicit env wins", config.ProviderRecordConfig{Name: "claude", Type: "anthropic", APIKeyEnv: "MY_OWN_VARIABLE"}, "own-key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveProviderKey(tc.pc, false, "", get); got != tc.want {
				t.Errorf("resolveProviderKey = %q, want %q", got, tc.want)
			}
		})
	}

	// Google's documented variable is accepted when GEMINI_API_KEY is absent.
	delete(env, "GEMINI_API_KEY")
	if got := resolveProviderKey(config.ProviderRecordConfig{Name: "gem", Type: "gemini"}, false, "", get); got != "google-key" {
		t.Errorf("GOOGLE_API_KEY fallback = %q", got)
	}
}
