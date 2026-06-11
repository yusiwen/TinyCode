package tool

import (
	"context"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/skill"
)

func TestSkillManageList(t *testing.T) {
	sm := SkillManage()
	result, err := sm.Execute(context.Background(), map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("list error: %v", err)
	}
	if !strings.Contains(result, "code-review") {
		t.Error("expected code-review in list")
	}
	if !strings.Contains(result, "git-commit") {
		t.Error("expected git-commit in list")
	}
	if !strings.Contains(result, "builtin") {
		t.Error("expected 'builtin' label in list")
	}
}

func TestSkillManageCreateAndDelete(t *testing.T) {
	skill.ResetLoaded()
	sm := SkillManage()

	// Create
	content := `---
name: manage-test
description: Skill manage test
---
## Steps
1. Test
`
	result, err := sm.Execute(context.Background(), map[string]any{
		"action":  "create",
		"name":    "manage-test",
		"content": content,
	})
	if err != nil {
		t.Fatalf("create error: %v", err)
	}
	if !strings.Contains(result, "Created") {
		t.Errorf("expected 'Created' in result, got %q", result)
	}
	defer skill.DeleteOne("manage-test")

	// Verify in list
	listResult, _ := sm.Execute(context.Background(), map[string]any{"action": "list"})
	if !strings.Contains(listResult, "manage-test") {
		t.Error("expected manage-test in list after create")
	}

	// Delete
	delResult, err := sm.Execute(context.Background(), map[string]any{
		"action": "delete",
		"name":   "manage-test",
	})
	if err != nil {
		t.Fatalf("delete error: %v", err)
	}
	if !strings.Contains(delResult, "Deleted") {
		t.Errorf("expected 'Deleted' in result, got %q", delResult)
	}
}

func TestSkillManageEdit(t *testing.T) {
	skill.ResetLoaded()
	sm := SkillManage()

	// Edit builtin (creates override)
	override := `---
name: code-review
description: Edited code review
---
## Custom Steps
1. Custom review
`
	result, err := sm.Execute(context.Background(), map[string]any{
		"action":  "edit",
		"name":    "code-review",
		"content": override,
	})
	if err != nil {
		t.Fatalf("edit error: %v", err)
	}
	if !strings.Contains(result, "Updated") {
		t.Errorf("expected 'Updated' in result, got %q", result)
	}
	defer skill.DeleteOne("code-review")
}

func TestSkillManageCreateEmptyContent(t *testing.T) {
	sm := SkillManage()
	_, err := sm.Execute(context.Background(), map[string]any{
		"action": "create",
		"name":   "empty-test",
	})
	if err == nil {
		t.Fatal("expected error with empty content")
	}
}

func TestSkillManageUnknownAction(t *testing.T) {
	sm := SkillManage()
	_, err := sm.Execute(context.Background(), map[string]any{"action": "unknown"})
	if err == nil {
		t.Fatal("expected error for unknown action")
	}
	if !strings.Contains(err.Error(), "unknown action") {
		t.Errorf("expected 'unknown action' in error, got %q", err)
	}
}

func TestSkillManageDeleteNoName(t *testing.T) {
	sm := SkillManage()
	_, err := sm.Execute(context.Background(), map[string]any{"action": "delete"})
	if err == nil {
		t.Fatal("expected error for delete without name")
	}
}
