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

// ── Fuzzy matching tests ──

func TestEditFuzzyLineTrimmed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("func main() {\n	return nil  \n}\n"), 0644)

	// Search with no trailing spaces
	edits := []editOp{{OldString: "	return nil\n", NewString: "	return 42\n"}}
	b, _ := json.Marshal(edits)
	tool := Edit()
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"edits": json.RawMessage(b),
	})
	if err != nil {
		t.Fatalf("edit error: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "return 42") {
		t.Errorf("expected 'return 42', got: %s", string(data))
	}
}

func TestEditFuzzyWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("func  main()	{\n	return\n}\n"), 0644)

	edits := []editOp{{OldString: "func main() {", NewString: "func main() int {"}}
	b, _ := json.Marshal(edits)
	tool := Edit()
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"edits": json.RawMessage(b),
	})
	if err != nil {
		t.Fatalf("edit error: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "func main() int {") {
		t.Errorf("expected 'func main() int {', got: %s", string(data))
	}
}

func TestEditFuzzyIndentFlexible(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("func main() {\n    return\n}\n"), 0644)

	// Search with different indentation (tabs vs spaces)
	edits := []editOp{{OldString: "	return\n", NewString: "	return 42\n"}}
	b, _ := json.Marshal(edits)
	tool := Edit()
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"edits": json.RawMessage(b),
	})
	if err != nil {
		t.Fatalf("edit error: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "return 42") {
		t.Errorf("expected 'return 42', got: %s", string(data))
	}
}

func TestEditFuzzyEscapeNormalized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("line one\nline two\n"), 0644)

	// Search with literal \n instead of real newlines
	edits := []editOp{{OldString: "line one\\nline two", NewString: "line one\\nline 2"}}
	b, _ := json.Marshal(edits)
	tool := Edit()
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"edits": json.RawMessage(b),
	})
	if err != nil {
		t.Fatalf("edit error: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "line 2") {
		t.Errorf("expected 'line 2', got: %s", string(data))
	}
}

func TestEditFuzzyUnicodeNormalized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("Version: \"1.0\"\n"), 0644)

	// Search with smart quotes
	edits := []editOp{{OldString: "Version: \u201c1.0\u201d", NewString: "Version: \u201c2.0\u201d"}}
	b, _ := json.Marshal(edits)
	tool := Edit()
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"edits": json.RawMessage(b),
	})
	if err != nil {
		t.Fatalf("edit error: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "2.0") {
		t.Errorf("expected '2.0', got: %s", string(data))
	}
}

func TestEditFuzzyIndentationCorrection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.go")
	os.WriteFile(path, []byte("func test() {\n        return\n}\n"), 0644)

	// Search with tabs, file has spaces — should match fuzzy AND auto-correct indent
	edits := []editOp{{OldString: "	return\n", NewString: "	return 42\n"}}
	b, _ := json.Marshal(edits)
	tool := Edit()
	_, err := tool.Execute(context.Background(), map[string]any{
		"path":  path,
		"edits": json.RawMessage(b),
	})
	if err != nil {
		t.Fatalf("edit error: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "return 42") {
		t.Errorf("expected 'return 42', got:\n%s", string(data))
	}
}
