package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditSingle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("func main() {\n\treturn\n}\n"), 0644)

	edits := []editOp{{OldString: "\treturn\n", NewString: "\treturn nil\n"}}
	b, _ := json.Marshal(edits)
	tool := Edit()
	result, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"edits": json.RawMessage(b),
	})
	if err != nil {
		t.Fatalf("edit error: %v", err)
	}
	if !strings.Contains(result, "Applied 1 edit") {
		t.Errorf("expected 'Applied 1 edit', got %q", result)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "return nil") {
		t.Errorf("expected 'return nil' in result file, got:\n%s", string(data))
	}
}

func TestEditMultiple(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	content := `func a() int { return 1 }
func b() int { return 2 }
func c() int { return 3 }
`
	os.WriteFile(path, []byte(content), 0644)

	edits := []editOp{
		{OldString: "return 1", NewString: "return 10"},
		{OldString: "return 2", NewString: "return 20"},
		{OldString: "return 3", NewString: "return 30"},
	}
	b, _ := json.Marshal(edits)
	tool := Edit()
	result, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"edits": json.RawMessage(b),
	})
	if err != nil {
		t.Fatalf("edit error: %v", err)
	}
	if !strings.Contains(result, "Applied 3 edit") {
		t.Errorf("expected 'Applied 3 edit', got %q", result)
	}

	data, _ := os.ReadFile(path)
	for _, v := range []string{"return 10", "return 20", "return 30"} {
		if !strings.Contains(string(data), v) {
			t.Errorf("expected %q in result", v)
		}
	}
}

func TestEditNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("original text\n"), 0644)

	edits := []editOp{{OldString: "nonexistent", NewString: "replacement"}}
	b, _ := json.Marshal(edits)
	tool := Edit()
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"edits": json.RawMessage(b),
	})
	if err == nil {
		t.Fatal("expected error for not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

func TestEditMultipleMatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	content := "x := 1\ny := 1\nz := 1\n"
	os.WriteFile(path, []byte(content), 0644)

	edits := []editOp{{OldString: ":= 1", NewString: ":= 42"}}
	b, _ := json.Marshal(edits)
	tool := Edit()
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"edits": json.RawMessage(b),
	})
	if err == nil {
		t.Fatal("expected error for multiple matches")
	}
	if !strings.Contains(err.Error(), "appears 3 times") {
		t.Errorf("expected 'appears 3 times' error, got %v", err)
	}
}

func TestEditWithContextDisambiguates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	content := `func a() { return 1 }
func b() { return 1 }
`
	os.WriteFile(path, []byte(content), 0644)

	// Provide enough context to match uniquely
	edits := []editOp{{OldString: "func a() { return 1 }", NewString: "func a() { return 42 }"}}
	b, _ := json.Marshal(edits)
	tool := Edit()
	result, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"edits": json.RawMessage(b),
	})
	if err != nil {
		t.Fatalf("edit error: %v", err)
	}
	if !strings.Contains(result, "Applied 1 edit") {
		t.Errorf("expected success, got %q", result)
	}
}

func TestEditEmptyOldString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("content\n"), 0644)

	edits := []editOp{{NewString: "new"}}
	b, _ := json.Marshal(edits)
	tool := Edit()
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"edits": json.RawMessage(b),
	})
	if err == nil {
		t.Fatal("expected error for empty old_string")
	}
}

func TestEditNoEdits(t *testing.T) {
	tool := Edit()
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":  "test.go",
		"edits": json.RawMessage(`[]`),
	})
	if err == nil {
		t.Fatal("expected error for empty edits")
	}
}

func TestEditMissingPath(t *testing.T) {
	tool := Edit()
	_, err := tool.Execute(context.Background(), map[string]any{
		"edits": json.RawMessage(`[{"old_string":"x","new_string":"y"}]`),
	})
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}
