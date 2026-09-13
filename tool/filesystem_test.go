package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/agent"
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

// TestSearchFilesRespectsSandbox verifies that search_files cannot read file
// contents outside the project root (it used to bypass the sandbox entirely).
func TestSearchFilesRespectsSandbox(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secrets.env"), []byte("API_KEY=super-secret-value\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("INSIDE_TOKEN here\n"), 0644); err != nil {
		t.Fatal(err)
	}

	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{ProjectRoot: root, allowedPaths: make(map[string]bool)}
	defer func() { DefaultSandbox = saved }()
	defer CancelPendingPermission()

	search := SearchFiles()

	// Outside the root: no user is available, so the gate must refuse and the
	// secret must not appear in the output.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	out, err := search.Execute(ctx, map[string]any{"pattern": "API_KEY", "path": outside})
	if err == nil {
		t.Fatalf("expected the sandbox to refuse an out-of-root search, got %q", out)
	}
	if strings.Contains(out, "super-secret-value") {
		t.Fatal("search leaked file contents from outside the project root")
	}

	// Inside the root the search still works.
	out, err = search.Execute(context.Background(), map[string]any{"pattern": "INSIDE_TOKEN", "path": root})
	if err != nil {
		t.Fatalf("in-root search failed: %v", err)
	}
	if !strings.Contains(out, "INSIDE_TOKEN") {
		t.Fatalf("expected the in-root search to match, got %q", out)
	}
}

// TestWithPathGate covers the wrapper used for tools implemented outside this
// package (the lsp_* tools).
func TestWithPathGate(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0755); err != nil {
		t.Fatal(err)
	}

	saved := DefaultSandbox
	DefaultSandbox = &SandboxConfig{ProjectRoot: root, allowedPaths: make(map[string]bool)}
	defer func() { DefaultSandbox = saved }()
	defer CancelPendingPermission()

	executed := 0
	inner := func(ctx context.Context, args map[string]any) (string, error) {
		executed++
		return "inner ran", nil
	}
	gated := WithPathGate(agent.Tool{
		Name:        "fake_lsp",
		Description: "test",
		Parameters:  map[string]any{},
		Execute:     inner,
	})

	// Out-of-root file_path: refused, inner never runs.
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if _, err := gated.Execute(ctx, map[string]any{"file_path": filepath.Join(outside, "x.go")}); err == nil {
		t.Error("expected the gate to refuse an out-of-root path")
	}
	if executed != 0 {
		t.Errorf("inner tool ran despite the denial (%d times)", executed)
	}

	// In-root path: passes through.
	if _, err := gated.Execute(context.Background(), map[string]any{"file_path": filepath.Join(root, "x.go")}); err != nil {
		t.Fatalf("in-root call failed: %v", err)
	}
	if executed != 1 {
		t.Errorf("inner executed %d times, want 1", executed)
	}

	// Tools without a path argument are never gated.
	if _, err := gated.Execute(context.Background(), map[string]any{}); err != nil {
		t.Fatalf("call without a path failed: %v", err)
	}
	if executed != 2 {
		t.Errorf("inner executed %d times, want 2", executed)
	}
}
