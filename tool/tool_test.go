package tool

import (
	"context"
	"testing"
)

func TestRegistry_RegisterAndList(t *testing.T) {
	r := NewRegistry()
	r.Register(Tool{
		Name:        "test_tool",
		Description: "a test tool",
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			return "done", nil
		},
	})

	tools := r.List()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "test_tool" {
		t.Fatalf("expected name 'test_tool', got %q", tools[0].Name)
	}
}

func TestRegistry_Find(t *testing.T) {
	r := NewRegistry()
	r.Register(Tool{Name: "alpha"})
	r.Register(Tool{Name: "beta"})

	tool, ok := r.Find("alpha")
	if !ok {
		t.Fatal("expected to find 'alpha'")
	}
	if tool.Name != "alpha" {
		t.Fatalf("expected name 'alpha', got %q", tool.Name)
	}

	_, ok = r.Find("gamma")
	if ok {
		t.Fatal("expected Find to return false for unknown tool")
	}
}

func TestRegistry_FindEmpty(t *testing.T) {
	r := NewRegistry()
	_, ok := r.Find("nothing")
	if ok {
		t.Fatal("expected Find to return false on empty registry")
	}
}

func TestRegistry_ListReturnsCopy(t *testing.T) {
	r := NewRegistry()
	r.Register(Tool{Name: "original"})

	tools := r.List()
	tools[0].Name = "mutated"
	// original registry should be unchanged
	found, _ := r.Find("original")
	if found.Name != "original" {
		t.Fatal("List should return a copy that does not affect the registry")
	}
}
