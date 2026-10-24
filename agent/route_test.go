package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// TestParseRoute pins the split the whole routing feature rests on: the first
// separator is the route boundary, and a model name may contain separators of its
// own.
func TestParseRoute(t *testing.T) {
	tests := []struct {
		value     string
		route     string
		model     string
		wantError string
	}{
		{value: ""},
		{value: "deepseek-v4-pro", model: "deepseek-v4-pro"},
		{value: "deepseek/deepseek-v4-pro", route: "deepseek", model: "deepseek-v4-pro"},
		{value: "openrouter/anthropic/claude-sonnet-4", route: "openrouter", model: "anthropic/claude-sonnet-4"},
		{value: "  deepseek/m  ", route: "deepseek", model: "m"},
		{value: "deepseek/", wantError: "the model after"},
		{value: "/m", wantError: "the route before"},
	}
	for _, tc := range tests {
		route, model, err := ParseRoute(tc.value)
		if tc.wantError != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("ParseRoute(%q) error = %v, want it to name %q", tc.value, err, tc.wantError)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRoute(%q): %v", tc.value, err)
			continue
		}
		if route != tc.route || model != tc.model {
			t.Errorf("ParseRoute(%q) = (%q, %q), want (%q, %q)", tc.value, route, model, tc.route, tc.model)
		}
	}
}

// TestAgentCallsThePinnedRoute is the acceptance for the provider half of A5: an
// agent whose configuration names a route reaches that provider, with the model
// half of the setting, even though the session's active provider is another one.
func TestAgentCallsThePinnedRoute(t *testing.T) {
	var gotRoute, gotModel string
	first := &routeMockProvider{
		MockProvider: &MockProvider{ChatFunc: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
			gotRoute = "first"
			return &types.ChatResponse{Content: "first"}, nil
		}},
		route: "first", model: "m1",
	}
	second := &routeMockProvider{
		MockProvider: &MockProvider{ChatFunc: func(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			gotRoute, gotModel = "second", req.Model
			return &types.ChatResponse{Content: "second"}, nil
		}},
		route: "second", model: "m2",
	}
	reg := NewProviderRegistry([]ProviderRecord{
		{Name: "first", Provider: first},
		{Name: "second", Provider: second},
	})

	a := &Agent{
		// The session is on "first"; the agent's pin must win.
		Provider: first,
		Resolver: reg,
		Config:   &AgentConfig{Name: "build", Route: "second", Model: "m2"},
		MaxSteps: 3, MaxTokens: 16,
	}
	answer, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if answer != "second" {
		t.Errorf("answer = %q, want the pinned route's", answer)
	}
	if gotRoute != "second" || gotModel != "m2" {
		t.Errorf("pinned call went to %q with model %q, want second/m2", gotRoute, gotModel)
	}
	if key, ok := a.priceKey(); !ok || key != "second/m2" {
		t.Errorf("priceKey = %q, %v; a pinned call is priced by the pinned route", key, ok)
	}
}

// TestPinnedRouteWithoutResolverIsAnError: silently using the session provider
// would be the exact bug this feature removes, so the failure is explicit.
func TestPinnedRouteWithoutResolverIsAnError(t *testing.T) {
	a := &Agent{
		Provider: &MockProvider{},
		Config:   &AgentConfig{Name: "build", Route: "second"},
		MaxSteps: 2, MaxTokens: 8,
	}
	_, err := a.Run(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "no provider resolver") {
		t.Fatalf("Run error = %v, want a refusal naming the missing resolver", err)
	}
}

// TestResolveUnknownRoute: the registry refuses a name nobody configured; the
// caller turns that into a startup error (see main_test.go).
func TestResolveUnknownRoute(t *testing.T) {
	reg := NewProviderRegistry([]ProviderRecord{{Name: "a", Provider: &MockProvider{}}})
	if _, err := reg.Resolve("b"); err == nil || !strings.Contains(err.Error(), `no provider named "b"`) {
		t.Errorf("Resolve(unknown) error = %v", err)
	}
	if p, err := reg.Resolve(""); err != nil || p == nil {
		t.Errorf("Resolve(empty) = %v, %v; want the active provider", p, err)
	}
	if _, err := (*ProviderRegistry)(nil).Resolve("a"); err == nil {
		t.Error("a nil registry must refuse to resolve")
	}
	if got := (*ProviderRegistry)(nil).CurrentRoute(); got != "" {
		t.Errorf("nil CurrentRoute = %q", got)
	}
}
