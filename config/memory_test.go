package config

import (
	"strings"
	"testing"
)

// TestMemoryDefaults pins what an absent section means: memory on, local, in the
// user's home, bounded. It is the state every existing install wakes up in, so
// the values are part of the contract rather than an implementation detail.
func TestMemoryDefaults(t *testing.T) {
	spec := Config{}.MemorySpec()
	if !spec.Enabled {
		t.Fatal("memory is off with no section at all")
	}
	if spec.Backend != MemoryBackendLocal {
		t.Fatalf("default backend = %q, want local", spec.Backend)
	}
	if spec.WantsNetwork() {
		t.Fatal("the default configuration sends text off the machine")
	}
	if !strings.HasSuffix(spec.Dir, ".tinycode/memory") {
		t.Fatalf("default dir = %q, want one under ~/.tinycode/memory", spec.Dir)
	}
	if spec.MaxEntries != DefaultMemoryMaxEntries || spec.MaxBytes != DefaultMemoryMaxBytes {
		t.Fatalf("bounds = %d/%d, want %d/%d",
			spec.MaxEntries, spec.MaxBytes, DefaultMemoryMaxEntries, DefaultMemoryMaxBytes)
	}
	if spec.RecallLimit != DefaultMemoryRecallLimit {
		t.Fatalf("recall limit = %d, want %d", spec.RecallLimit, DefaultMemoryRecallLimit)
	}
}

// TestMemorySectionResolves pins the values a section can set, and the two
// deliberate readings: a typo is local rather than an error, and "off" is
// expressed with a pointer so an absent field cannot switch it back on.
func TestMemorySectionResolves(t *testing.T) {
	off := false
	spec := Config{Memory: &MemoryConfig{
		Enabled:     &off,
		Backend:     "Hindsight", // matched case-insensitively
		Dir:         "/tmp/store",
		BaseURL:     "http://127.0.0.1:9070",
		Bank:        "work",
		MaxEntries:  10,
		MaxBytes:    1024,
		RecallLimit: 2,
	}}.MemorySpec()

	if spec.Enabled {
		t.Fatal("enabled: false was ignored")
	}
	if spec.Backend != MemoryBackendHindsight {
		t.Fatalf("backend = %q, want hindsight", spec.Backend)
	}
	if !spec.WantsNetwork() {
		t.Fatal("a network backend was not reported as egress")
	}
	if spec.Dir != "/tmp/store" || spec.Bank != "work" {
		t.Fatalf("section = %+v", spec)
	}
	if spec.MaxEntries != 10 || spec.MaxBytes != 1024 || spec.RecallLimit != 2 {
		t.Fatalf("bounds were not taken from the section: %+v", spec)
	}

	typo := Config{Memory: &MemoryConfig{Backend: "hinsight"}}.MemorySpec()
	if typo.Backend != MemoryBackendLocal || typo.WantsNetwork() {
		t.Fatalf("a misspelled backend became %q", typo.Backend)
	}
}

// TestResolveMemoryHonoursEgressOnlyFromTheUser pins the rule the sandbox uses
// for boundary keys: a project-local file must not decide that the prompt goes
// to another machine, and the person who wrote it can see that it did not.
func TestResolveMemoryHonoursEgressOnlyFromTheUser(t *testing.T) {
	projectOnly := &Config{Memory: &MemoryConfig{
		Backend: MemoryBackendHindsight,
		BaseURL: "https://memory.example.com",
	}}
	spec, ignored := UserLayer{raw: map[string]any{}}.ResolveMemory(projectOnly)
	if !ignored {
		t.Fatal("a project-local egress request was not reported as ignored")
	}
	if spec.Backend != MemoryBackendLocal || spec.BaseURL != "" {
		t.Fatalf("the fallback still names a network backend: %+v", spec)
	}

	userAsks := UserLayer{raw: map[string]any{
		"memory": map[string]any{"backend": "hindsight"},
	}}
	spec, ignored = userAsks.ResolveMemory(projectOnly)
	if ignored {
		t.Fatal("the user's own egress request was ignored")
	}
	if spec.Backend != MemoryBackendHindsight || spec.BaseURL != "https://memory.example.com" {
		t.Fatalf("the user's request did not resolve: %+v", spec)
	}

	// A base_url alone is egress too, whatever the backend field says.
	projectURL := &Config{Memory: &MemoryConfig{BaseURL: "https://memory.example.com"}}
	if _, ignored := (UserLayer{raw: map[string]any{}}).ResolveMemory(projectURL); !ignored {
		t.Fatal("a project-local base_url was honoured")
	}
	userURL := UserLayer{raw: map[string]any{
		"memory": map[string]any{"base_url": "https://memory.example.com"},
	}}
	if _, ignored := userURL.ResolveMemory(projectURL); ignored {
		t.Fatal("the user's own base_url was ignored")
	}
}

// TestResolveMemoryHonoursNarrowingFromAnyLayer pins the other direction: off is
// not a widening, so a project may switch memory off for itself.
func TestResolveMemoryHonoursNarrowingFromAnyLayer(t *testing.T) {
	off := false
	project := merge(Config{}, Config{Memory: &MemoryConfig{Enabled: &off}})
	if spec, ignored := (UserLayer{raw: map[string]any{}}).ResolveMemory(&project); spec.Enabled || ignored {
		t.Fatalf("a project could not switch memory off: enabled=%v ignored=%v", spec.Enabled, ignored)
	}

	// A directory override is storage, not egress, and is honoured from any layer.
	projectDir := merge(Config{}, Config{Memory: &MemoryConfig{Dir: "/tmp/elsewhere"}})
	spec, ignored := (UserLayer{raw: map[string]any{}}).ResolveMemory(&projectDir)
	if spec.Dir != "/tmp/elsewhere" || ignored {
		t.Fatalf("a project directory was not honoured: %+v ignored=%v", spec, ignored)
	}
}
