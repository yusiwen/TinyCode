package tool

import (
	"context"
	"strings"
	"testing"
)

func TestBash_NameAndDescription(t *testing.T) {
	b := Bash()
	if b.Name != "bash" {
		t.Fatalf("expected Name 'bash', got %q", b.Name)
	}
	if b.Description == "" {
		t.Fatal("Description should not be empty")
	}
}

func TestBash_ExecuteEcho(t *testing.T) {
	b := Bash()
	ctx := context.Background()
	result, err := b.Execute(ctx, map[string]any{
		"command": "echo hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "hello") {
		t.Fatalf("expected output to contain 'hello', got %q", result)
	}
}

func TestBash_ExecuteWithTimeout(t *testing.T) {
	b := Bash()
	ctx := context.Background()
	// Use a short timeout with a fast command to verify the timeout parameter is accepted
	result, err := b.Execute(ctx, map[string]any{
		"command": "echo timeout-test",
		"timeout": float64(5),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "timeout-test") {
		t.Fatalf("expected output to contain 'timeout-test', got %q", result)
	}
}

func TestBash_ExecuteTimeoutExceeded(t *testing.T) {
	b := Bash()
	ctx := context.Background()
	result, _ := b.Execute(ctx, map[string]any{
		"command": "sleep 10",
		"timeout": float64(1),
	})
	if !strings.Contains(result, "killed") && !strings.Contains(result, "signal") &&
		!strings.Contains(result, "deadline") {
		t.Fatalf("expected timeout-related message, got %q", result)
	}
}

func TestBash_ExecuteEmptyCommand(t *testing.T) {
	b := Bash()
	ctx := context.Background()
	_, err := b.Execute(ctx, map[string]any{
		"command": "",
	})
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestBash_ExecuteWithWorkdir(t *testing.T) {
	b := Bash()
	ctx := context.Background()
	result, err := b.Execute(ctx, map[string]any{
		"command": "pwd",
		"workdir": "/tmp",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "/tmp") {
		t.Fatalf("expected output to contain '/tmp', got %q", result)
	}
}
