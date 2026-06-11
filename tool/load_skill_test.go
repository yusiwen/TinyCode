package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/skill"
)

func TestLoadSkill(t *testing.T) {
	skill.ResetLoaded()
	ls := LoadSkill()

	if ls.Name != "load_skill" {
		t.Errorf("expected name=load_skill, got %q", ls.Name)
	}
	if ls.Description == "" {
		t.Error("expected non-empty description")
	}

	// Execute with known skill
	result, err := ls.Execute(context.Background(), map[string]any{"name": "code-review"})
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result for code-review skill")
	}
	if !strings.Contains(result, "Steps") {
		t.Error("expected Steps section in result")
	}
}

func TestLoadSkillDedup(t *testing.T) {
	skill.ResetLoaded()
	ls := LoadSkill()

	// First call — should return content
	r1, _ := ls.Execute(context.Background(), map[string]any{"name": "code-review"})
	if r1 == "" {
		t.Fatal("expected content on first call")
	}

	// Second call — should return empty (dedup)
	r2, _ := ls.Execute(context.Background(), map[string]any{"name": "code-review"})
	if r2 != "" {
		t.Errorf("expected empty on dedup call, got %q", r2)
	}
}

func TestLoadSkillNotFound(t *testing.T) {
	skill.ResetLoaded()
	ls := LoadSkill()

	result, err := ls.Execute(context.Background(), map[string]any{"name": "nonexistent"})
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty for nonexistent skill, got %q", result)
	}
}

func TestLoadSkillEmptyName(t *testing.T) {
	skill.ResetLoaded()
	ls := LoadSkill()

	result, err := ls.Execute(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("execute error: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty for empty name, got %q", result)
	}
}
