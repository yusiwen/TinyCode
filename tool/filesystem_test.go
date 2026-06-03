package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFile_Name(t *testing.T) {
	tool := ReadFile()
	if tool.Name != "read_file" {
		t.Fatalf("expected Name 'read_file', got %q", tool.Name)
	}
}

func TestWriteFile_Name(t *testing.T) {
	tool := WriteFile()
	if tool.Name != "write_file" {
		t.Fatalf("expected Name 'write_file', got %q", tool.Name)
	}
}

func TestSearchFiles_Name(t *testing.T) {
	tool := SearchFiles()
	if tool.Name != "search_files" {
		t.Fatalf("expected Name 'search_files', got %q", tool.Name)
	}
}

func TestReadFile_Execute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "hello\nworld"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := ReadFile()
	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"path": path,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "hello") {
		t.Fatalf("expected result to contain 'hello', got %q", result)
	}
	if !strings.Contains(result, "world") {
		t.Fatalf("expected result to contain 'world', got %q", result)
	}
	if !strings.Contains(result, "=== ") {
		t.Fatalf("expected result to contain header '=== ', got %q", result)
	}
}

func TestReadFile_ExecuteMissingPath(t *testing.T) {
	tool := ReadFile()
	ctx := context.Background()
	_, err := tool.Execute(ctx, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}

func TestWriteFile_Execute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "new.txt")

	tool := WriteFile()
	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"path":    path,
		"content": "test content",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "Wrote") {
		t.Fatalf("expected result to contain 'Wrote', got %q", result)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "test content" {
		t.Fatalf("expected file content 'test content', got %q", string(data))
	}
}

func TestSearchFiles_ExecuteNative(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\nfunc foo() {}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.py"), []byte("def foo():\n    pass"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := SearchFiles()
	ctx := context.Background()
	result, err := tool.Execute(ctx, map[string]any{
		"pattern": "foo",
		"path":    dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result, "foo") {
		t.Fatalf("expected result to contain 'foo', got %q", result)
	}
}
