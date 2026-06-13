package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte("func main() {\n\treturn\n}\n"), 0644)

	patch := `*** Begin Patch
*** Update File: ` + path + `
@@ update return @@
 func main() {
-	return
+	return nil
 }
*** End Patch`

	tool := ApplyPatch()
	result, err := tool.Execute(context.Background(), map[string]any{
		"patch_text": patch,
	})
	if err != nil {
		t.Fatalf("apply_patch error: %v", err)
	}
	if !strings.Contains(result, "U ") {
		t.Errorf("expected 'U ' in result, got %q", result)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "return nil") {
		t.Errorf("expected 'return nil' in file, got:\n%s", string(data))
	}
}

func TestApplyPatchAddFile(t *testing.T) {
	dir := t.TempDir()
	newPath := filepath.Join(dir, "helper.go")

	patch := `*** Begin Patch
*** Add File: ` + newPath + `
+package main
+func Helper() string {
+    return "hello"
+}
*** End Patch`

	tool := ApplyPatch()
	result, err := tool.Execute(context.Background(), map[string]any{
		"patch_text": patch,
	})
	if err != nil {
		t.Fatalf("apply_patch error: %v", err)
	}
	if !strings.Contains(result, "A ") {
		t.Errorf("expected 'A ' in result, got %q", result)
	}
	data, _ := os.ReadFile(newPath)
	if !strings.Contains(string(data), "func Helper()") {
		t.Errorf("expected func Helper in created file, got:\n%s", string(data))
	}
}

func TestApplyPatchDeleteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.go")
	os.WriteFile(path, []byte("package main\n"), 0644)

	patch := `*** Begin Patch
*** Delete File: ` + path + `
*** End Patch`

	tool := ApplyPatch()
	result, err := tool.Execute(context.Background(), map[string]any{
		"patch_text": patch,
	})
	if err != nil {
		t.Fatalf("apply_patch error: %v", err)
	}
	if !strings.Contains(result, "D ") {
		t.Errorf("expected 'D ' in result, got %q", result)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to be deleted")
	}
}

func TestApplyPatchMultiFile(t *testing.T) {
	dir := t.TempDir()
	f1 := filepath.Join(dir, "a.go")
	f2 := filepath.Join(dir, "b.go")
	os.WriteFile(f1, []byte("package main\nfunc a() int { return 0 }\n"), 0644)
	os.WriteFile(f2, []byte("package main\nfunc b() int { return 0 }\n"), 0644)

	patch := `*** Begin Patch
*** Update File: ` + f1 + `
@@ fix a @@
-func a() int { return 0 }
+func a() int { return 42 }
*** Update File: ` + f2 + `
@@ fix b @@
-func b() int { return 0 }
+func b() int { return 99 }
*** End Patch`

	tool := ApplyPatch()
	result, err := tool.Execute(context.Background(), map[string]any{
		"patch_text": patch,
	})
	if err != nil {
		t.Fatalf("apply_patch error: %v", err)
	}
	if !strings.Contains(result, "U ") {
		t.Errorf("expected 'U ' in result, got %q", result)
	}
	if !strings.Contains(result, "2 operation") {
		t.Errorf("expected '2 operation' in summary, got %q", result)
	}
}

func TestApplyPatchHeredoc(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	os.WriteFile(path, []byte("func main() {\n\treturn\n}\n"), 0644)

	// Simulate heredoc style (indented ***)
	patch := `*** Begin Patch
*** Update File: ` + path + `
@@ fix return @@
 func main() {
-	return
+	return nil
 }
*** End Patch`

	tool := ApplyPatch()
	result, err := tool.Execute(context.Background(), map[string]any{
		"patch_text": patch,
	})
	if err != nil {
		t.Fatalf("apply_patch error: %v", err)
	}
	if !strings.Contains(result, "U ") {
		t.Errorf("expected 'U ' in result, got %q", result)
	}
}

func TestApplyPatchInvalidFormat(t *testing.T) {
	tool := ApplyPatch()
	_, err := tool.Execute(context.Background(), map[string]any{
		"patch_text": "not a valid patch",
	})
	if err == nil || !strings.Contains(err.Error(), "no operations") {
		t.Errorf("expected 'no operations' error, got %v", err)
	}
}

func TestApplyPatchEmptyText(t *testing.T) {
	tool := ApplyPatch()
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for empty patch_text")
	}
}

func TestApplyPatchDeleteNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.go")

	patch := `*** Begin Patch
*** Delete File: ` + path + `
*** End Patch`

	tool := ApplyPatch()
	_, err := tool.Execute(context.Background(), map[string]any{
		"patch_text": patch,
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

func TestApplyPatchAllOps(t *testing.T) {
	dir := t.TempDir()
	updatePath := filepath.Join(dir, "update.go")
	os.WriteFile(updatePath, []byte("package main\nfunc old() {}\n"), 0644)
	addPath := filepath.Join(dir, "add.go")
	deletePath := filepath.Join(dir, "delete.go")
	os.WriteFile(deletePath, []byte("package main\n"), 0644)

	patch := `*** Begin Patch
*** Update File: ` + updatePath + `
@@ rename old to new @@
-func old() {}
+func new() {}
*** Add File: ` + addPath + `
+package main
+func newFunc() {}
*** Delete File: ` + deletePath + `
*** End Patch`

	tool := ApplyPatch()
	result, err := tool.Execute(context.Background(), map[string]any{
		"patch_text": patch,
	})
	if err != nil {
		t.Fatalf("apply_patch error: %v", err)
	}
	if !strings.Contains(result, "U ") || !strings.Contains(result, "A ") || !strings.Contains(result, "D ") {
		t.Errorf("expected U, A, D in result, got %q", result)
	}
	// Verify update
	data, _ := os.ReadFile(updatePath)
	if !strings.Contains(string(data), "func new()") {
		t.Error("update failed")
	}
	// Verify add
	data, _ = os.ReadFile(addPath)
	if !strings.Contains(string(data), "func newFunc()") {
		t.Error("add failed")
	}
	// Verify delete
	if _, err := os.Stat(deletePath); !os.IsNotExist(err) {
		t.Error("delete failed")
	}
}
