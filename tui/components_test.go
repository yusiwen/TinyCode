package tui

import (
	"strings"
	"testing"
)

func TestToolCallComponentSkipsTodo(t *testing.T) {
	msg := chatMessage{
		Role: "assistant",
		Content: "Let me plan this:",
		ToolCalls: []ToolCallInfo{
			{Name: "todo", Arg: `{"todos":[{"id":"1","content":"Create files","status":"in_progress"}]}`},
			{Name: "bash", Arg: "mkdir -p myproject"},
			{Name: "write_file", Arg: "pom.xml (10 lines)"},
		},
	}

	comp := ToolCallComponent{}
	chunks := comp.Render(msg, false)

	// Should include "→ Calling tools:" header
	headerFound := false
	for _, ch := range chunks {
		if strings.Contains(ch.Text, "→ Calling tools:") {
			headerFound = true
			break
		}
	}
	if !headerFound {
		t.Fatal("expected '→ Calling tools:' header")
	}

	// Should NOT include "todo" in any chunk
	for _, ch := range chunks {
		if strings.Contains(ch.Text, "todo") && !strings.Contains(ch.Text, "→ Calling tools:") {
			t.Fatalf("unexpected 'todo' in chunk: %q", ch.Text)
		}
	}

	// Should include bash and write_file
	bashFound := false
	writeFound := false
	for _, ch := range chunks {
		if strings.Contains(ch.Text, "• bash") {
			bashFound = true
		}
		if strings.Contains(ch.Text, "• write_file") {
			writeFound = true
		}
	}
	if !bashFound {
		t.Fatal("expected bash tool call in output")
	}
	if !writeFound {
		t.Fatal("expected write_file tool call in output")
	}
}

func TestToolCallComponentAllowsNonTodo(t *testing.T) {
	msg := chatMessage{
		Role: "assistant",
		ToolCalls: []ToolCallInfo{
			{Name: "bash", Arg: "ls -la"},
			{Name: "read_file", Arg: "main.go"},
		},
	}

	comp := ToolCallComponent{}
	chunks := comp.Render(msg, false)

	// Should include both tools
	bashFound := false
	readFound := false
	for _, ch := range chunks {
		if strings.Contains(ch.Text, "• bash") {
			bashFound = true
		}
		if strings.Contains(ch.Text, "• read_file") {
			readFound = true
		}
	}
	if !bashFound {
		t.Fatal("expected bash tool call")
	}
	if !readFound {
		t.Fatal("expected read_file tool call")
	}
}

func TestToolCallComponentEmpty(t *testing.T) {
	msg := chatMessage{
		Role:      "assistant",
		ToolCalls: []ToolCallInfo{},
	}

	comp := ToolCallComponent{}
	chunks := comp.Render(msg, false)

	// Should still have the header
	headerFound := false
	for _, ch := range chunks {
		if strings.Contains(ch.Text, "→ Calling tools:") {
			headerFound = true
			break
		}
	}
	if !headerFound {
		t.Fatal("expected '→ Calling tools:' header even with no tools")
	}
}
