package tui

import (
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/session"
	"github.com/yusiwen/tinycode/tool"
	"github.com/yusiwen/tinycode/types"
)

// twoRoutes returns a registry with two named providers, which is the minimum a
// route can be restored to.
func twoRoutes() (*agent.ProviderRegistry, *agent.MockProvider, *agent.MockProvider) {
	first, second := &agent.MockProvider{}, &agent.MockProvider{}
	return agent.NewProviderRegistry([]agent.ProviderRecord{
		{Name: "first", Provider: first},
		{Name: "second", Provider: second},
	}), first, second
}

func seedRouteSession(t *testing.T, dir, id, route string) {
	t.Helper()
	s := session.New(id, dir)
	if err := s.Append(types.Message{Role: types.RoleUser, Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	s.ProviderName = route
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
}

// TestResumeRestoresTheSavedProvider is issue #284's acceptance: the file names a
// configuration route, and resuming comes back on that route. The old code
// persisted the provider's display name ("openai/<model>") and matched it against
// the configuration name, so it never matched and the default was used silently.
func TestResumeRestoresTheSavedProvider(t *testing.T) {
	dir := t.TempDir()
	seedRouteSession(t, dir, "TUI-route", "second")

	reg, first, _ := twoRoutes()
	m := NewTUI(agent.New(first), &config.Config{SessionDir: dir}, agent.NewRegistry(),
		reg, tool.NewTodoStore(), nil, "TUI-route")

	if got := m.provReg.CurrentRoute(); got != "second" {
		t.Fatalf("resumed route = %q, want the one saved in the session", got)
	}
	if m.agent.Provider != m.provReg.Current() {
		t.Error("the agent's provider does not follow the restored route")
	}

	// The next save keeps the route, so a restore is not a one-off.
	m.persistSession()
	reloaded, err := session.Load("TUI-route", dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.ProviderName != "second" {
		t.Errorf("re-saved provider_name = %q, want second", reloaded.ProviderName)
	}
}

// TestResumeWithAnUnknownRouteWarns: a route removed from the configuration must
// not fail the resume, but must not pass in silence either.
func TestResumeWithAnUnknownRouteWarns(t *testing.T) {
	dir := t.TempDir()
	seedRouteSession(t, dir, "TUI-gone", "removed-route")

	reg, first, _ := twoRoutes()
	m := NewTUI(agent.New(first), &config.Config{SessionDir: dir}, agent.NewRegistry(),
		reg, tool.NewTodoStore(), nil, "TUI-gone")

	if got := m.provReg.CurrentRoute(); got != "first" {
		t.Fatalf("route after a missing one = %q, want the default", got)
	}
	var warned bool
	for _, msg := range m.messages {
		if msg.Role == "system" && strings.Contains(msg.Content, `Route "removed-route"`) {
			warned = true
			break
		}
	}
	if !warned {
		t.Errorf("no warning about the missing route; messages = %v", m.messages)
	}
}

// TestPersistRecordsTheRouteNotTheProviderDisplayName keeps the two namespaces
// from being confused again: `model_name` stays the display value and
// `provider_name` is what a restore can match.
func TestPersistRecordsTheRouteAndTheDisplayName(t *testing.T) {
	dir := t.TempDir()
	reg, first, _ := twoRoutes()
	m := NewTUI(agent.New(first), &config.Config{SessionDir: dir}, agent.NewRegistry(),
		reg, tool.NewTodoStore(), nil)
	m.messages = append(m.messages, chatMessage{Role: "user", Content: "hello"})
	m.persistSession()

	saves := session.NewStore(dir).List()
	if len(saves) != 1 {
		t.Fatalf("saved %d sessions, want 1", len(saves))
	}
	s, err := session.Load(saves[0].ID, dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.ProviderName != "first" {
		t.Errorf("provider_name = %q, want the configuration route", s.ProviderName)
	}
	if s.ModelName != first.Name() {
		t.Errorf("model_name = %q, want the provider's display name %q", s.ModelName, first.Name())
	}
}
