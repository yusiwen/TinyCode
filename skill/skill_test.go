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
	if !strings.Contains(names, "load_skill") {
		t.Error("expected load_skill tool usage hint")
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

func TestLoadOnceFirstCall(t *testing.T) {
	ResetLoaded()
	content, fresh := LoadOnce("code-review", "")
	if content == "" {
		t.Fatal("expected non-empty content on first load")
	}
	if !fresh {
		t.Error("expected fresh=true on first load")
	}
}

func TestLoadOnceDedup(t *testing.T) {
	ResetLoaded()
	_, fresh1 := LoadOnce("code-review", "")
	if !fresh1 {
		t.Fatal("expected first call to be fresh")
	}
	content2, fresh2 := LoadOnce("code-review", "")
	if content2 != "" {
		t.Errorf("expected empty content on repeat load, got %q", content2)
	}
	if fresh2 {
		t.Error("expected fresh=false on repeat call")
	}
}

func TestLoadOnceNotFound(t *testing.T) {
	ResetLoaded()
	content, fresh := LoadOnce("nonexistent-skill", "")
	if content != "" {
		t.Errorf("expected empty content for nonexistent skill, got %q", content)
	}
	if fresh {
		t.Error("expected fresh=false for nonexistent skill")
	}
}

func TestCreateAndDelete(t *testing.T) {
	ResetLoaded()
	content := `---
name: test-skill
description: A test skill
---
## Steps
1. Do something
`
	if _, err := CreateOne(content); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer DeleteOne("test-skill")

	// Verify it was created and can be discovered
	skills := Discover(".")
	found := false
	for _, s := range skills {
		if s.Name == "test-skill" {
			found = true
			if s.Builtin {
				t.Error("expected created skill to be user (not builtin)")
			}
			break
		}
	}
	if !found {
		t.Error("expected test-skill to appear in Discover")
	}

	// Verify LoadOnce works on created skill
	ResetOne("test-skill")
	content2, fresh := LoadOnce("test-skill", ".")
	if !fresh {
		t.Error("expected fresh=true after ResetOne")
	}
	if !strings.Contains(content2, "Do something") {
		t.Error("expected content to contain Steps")
	}
}

func TestEditOverrideBuiltin(t *testing.T) {
	ResetLoaded()
	override := `---
name: code-review
description: Custom code review override
---
## Custom Steps
1. User's custom review process
`
	isOverride, err := EditOne("code-review", override)
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !isOverride {
		t.Error("expected isOverride=true for builtin skill edit")
	}
	defer DeleteOne("code-review")

	// Verify discovery picks up the override
	skills := Discover(".")
	for _, s := range skills {
		if s.Name == "code-review" && !s.Builtin {
			return // found the user override
		}
	}
	t.Error("expected user override of code-review to be discoverable")
}

func TestDeleteBuiltinFails(t *testing.T) {
	err := DeleteOne("code-review")
	if err == nil {
		t.Fatal("expected error deleting builtin skill")
	}
	if !strings.Contains(err.Error(), "cannot delete builtin") {
		t.Errorf("expected 'cannot delete builtin' in error, got %q", err)
	}
}

func TestDeleteNonexistentFails(t *testing.T) {
	err := DeleteOne("nonexistent-skill")
	if err == nil {
		t.Fatal("expected error deleting nonexistent skill")
	}
}

func TestCreateEmptyContentFails(t *testing.T) {
	_, err := CreateOne("")
	if err == nil {
		t.Fatal("expected error with empty content")
	}
}

func TestListAll(t *testing.T) {
	all := ListAll(".")
	if len(all) < 2 {
		t.Fatalf("expected at least 2 skills, got %d", len(all))
	}
	hasBuiltin := false
	hasUser := false
	for _, s := range all {
		if s.Source == "builtin" {
			hasBuiltin = true
		}
		if s.Source == "user" {
			hasUser = true
		}
	}
	if !hasBuiltin {
		t.Error("expected builtin skills in ListAll")
	}
	// User skills may or may not exist depending on test environment
	_ = hasUser
}
