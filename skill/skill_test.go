package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverBuiltin(t *testing.T) {
	skills := Discover("")
	if len(skills) < 2 {
		t.Fatalf("expected at least 2 builtin skills, got %d", len(skills))
	}

	found := map[string]bool{}
	for _, s := range skills {
		found[s.Name] = true
		if !s.Builtin {
			t.Errorf("expected builtin=true for %s", s.Name)
		}
		if s.Description == "" {
			t.Errorf("expected non-empty description for %s", s.Name)
		}
		if s.Content == "" {
			t.Errorf("expected non-empty content for %s", s.Name)
		}
	}
	if !found["code-review"] {
		t.Error("expected code-review builtin skill")
	}
	if !found["git-commit"] {
		t.Error("expected git-commit builtin skill")
	}
}

func TestDiscoveredNames(t *testing.T) {
	names := DiscoveredNames("")
	if !strings.Contains(names, "code-review") {
		t.Error("expected code-review in discovered names")
	}
	if !strings.Contains(names, "git-commit") {
		t.Error("expected git-commit in discovered names")
	}
	if !strings.Contains(names, "Available skills") {
		t.Error("expected 'Available skills' header")
	}
	if !strings.Contains(names, "/skill") {
		t.Error("expected /skill usage hint")
	}
}

func TestFindByName(t *testing.T) {
	s := FindByName("code-review", "")
	if s == nil {
		t.Fatal("expected to find code-review")
	}
	if s.Name != "code-review" {
		t.Errorf("expected name=code-review, got %q", s.Name)
	}
	if s.Description == "" {
		t.Error("expected non-empty description")
	}
}

func TestFindByNameCaseInsensitive(t *testing.T) {
	s := FindByName("CODE-REVIEW", "")
	if s == nil || s.Name != "code-review" {
		t.Error("expected case-insensitive match for CODE-REVIEW")
	}
}

func TestLoadContent(t *testing.T) {
	content := LoadContent("code-review", "")
	if content == "" {
		t.Fatal("expected non-empty content for code-review")
	}
	if !strings.Contains(content, "Steps") {
		t.Error("expected Steps section in code-review content")
	}
	if !strings.Contains(content, "git diff") {
		t.Error("expected git diff instructions in code-review content")
	}
}

func TestUserSkillOverride(t *testing.T) {
	dir := t.TempDir()
	cwd := dir

	// Create a user skill that overrides builtin
	skillDir := filepath.Join(dir, ".tinycode", "skills", "code-review")
	os.MkdirAll(skillDir, 0755)
	userContent := `---
name: code-review
description: Custom code review
---

## Custom Steps
1. Do something different
`
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(userContent), 0644)

	// Discover from this project
	skills := Discover(cwd)
	found := false
	for _, s := range skills {
		if s.Name == "code-review" {
			found = true
			if s.Builtin {
				t.Error("expected user skill to override builtin (Builtin=false)")
			}
			if !strings.Contains(s.Content, "Custom Steps") {
				t.Error("expected user skill content to override builtin")
			}
			break
		}
	}
	if !found {
		t.Error("expected code-review to still exist via user override")
	}
}

func TestDiscoverNonexistentProject(t *testing.T) {
	// Discover from a temp dir with no .tinycode/skills/ should not add duplicates
	dir := t.TempDir()
	skills := Discover(dir)
	// Count occurrences of each name
	counts := map[string]int{}
	for _, s := range skills {
		counts[s.Name]++
	}
	for name, count := range counts {
		if count > 1 {
			t.Errorf("duplicate skill %s: found %d times", name, count)
		}
	}
}

func TestFindByNameNotFound(t *testing.T) {
	s := FindByName("nonexistent-skill", "")
	if s != nil {
		t.Errorf("expected nil for nonexistent skill, got %v", s)
	}
}
