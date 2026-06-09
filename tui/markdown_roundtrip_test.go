package tui

import (
	"strings"
	"testing"
)

func TestMarkdownRoundTrip(t *testing.T) {
	md := "**Key Points:**\n\n1. File structure: A single file `main.go` contains:\n2. HTTP Server setup using `net/http`\n3. Inline code like `http.HandleFunc(\"/\", func(w http.ResponseWriter, r *http.Request) {})`\n4. Response struct containing JSON fields (`message`)\n5. Request processing using an atomic channel"

	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	checks := []string{
		"Key Points",
		"File structure",
		"main.go",
		"net/http",
		"http.HandleFunc",
		"ResponseWriter",
		"JSON fields",
		"message",
		"atomic channel",
	}
	for _, s := range checks {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q\nOutput:\n%s", s, out)
		}
	}
}

func TestMarkdownRoundTripHeading(t *testing.T) {
	md := "## Section Title\n\nParagraph here.\n\n### Subsection\n\nMore text."
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	for _, s := range []string{"Section Title", "Paragraph here", "Subsection", "More text"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripCodeBlock(t *testing.T) {
	md := "Text before:\n\n```go\npackage main\n\nfunc main() {}\n```\n\nText after."
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	for _, s := range []string{"Text before", "Text after", "package main", "func main"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripCodeIndent(t *testing.T) {
	// Verify code block indentation is preserved (tabs → spaces)
	md := "```go\npackage main\n\nimport (\n	\"fmt\"\n	\"log\"\n)\n\nfunc main() {\n	log.Println(\"hi\")\n}\n```"
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	// Tabs should be converted to 4 spaces
	for _, s := range []string{`    "fmt"`, `    "log"`, `    log.Println`} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING indented code: %q\nOutput:\n%s", s, out)
		}
	}
	// Tabs should NOT appear in output
	if strings.Contains(out, "	") {
		t.Error("tabs should NOT appear in rendered output")
	}
}

func TestMarkdownRoundTripNestedList(t *testing.T) {
	md := "- Level 1\n  - Level 2\n    - Level 3\n      - Level 4"
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	for _, s := range []string{"Level 1", "Level 2", "Level 3", "Level 4"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripBlockquote(t *testing.T) {
	md := "Normal text.\n\n> Blockquote content here\n\nMore text."
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	for _, s := range []string{"Normal text", "Blockquote content here", "More text"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripItalic(t *testing.T) {
	md := "Normal *italic* and **bold** and ***both*** text."
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	for _, s := range []string{"Normal", "italic", "bold", "both", "text"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripTable(t *testing.T) {
	md := "| H1 | H2 | H3 |\n|---|---|---|\n| A | B | C |\n| D | E | F |"
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	for _, s := range []string{"H1", "H2", "H3", "A", "B", "C", "D", "E", "F"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripLink(t *testing.T) {
	md := "Click [here](https://example.com) for details."
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	for _, s := range []string{"Click", "here", "for details"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripHr(t *testing.T) {
	md := "Before\n\n---\n\nAfter"
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	for _, s := range []string{"Before", "After"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownRoundTripMixed(t *testing.T) {
	md := "# Document\n\nA paragraph with **bold**, *italic*, `code`.\n\n> A quote here\n\n- Item 1\n- Item 2\n\n```go\npackage main\n```\n\nDone."
	blocks := parseMarkdown(md)

	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					rendered.WriteString(chunk.Text)
					rendered.WriteByte('\n')
				}
			}
		}
	}
	out := rendered.String()

	for _, s := range []string{"Document", "bold", "italic", "code", "quote here",
		"Item 1", "Item 2", "package main", "Done"} {
		if !strings.Contains(out, s) {
			t.Errorf("MISSING: %q", s)
		}
	}
}

func TestMarkdownStreamingIncremental(t *testing.T) {
	partial := "Hello **world"
	blocks := parseMarkdown(partial)
	if len(blocks) == 0 {
		t.Fatal("parseMarkdown should return blocks even for partial content")
	}
	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					chunk := chunk
					rendered.WriteString(chunk.Text)
					rendered.WriteString("\n")
				}
			}
		}
	}
	out := rendered.String()
	if !strings.Contains(out, "Hello") {
		t.Errorf("MISSING: %q", "Hello")
	}
	if !strings.Contains(out, "world") {
		t.Errorf("MISSING: %q", "world")
	}
	// Now close the bold
	full := "Hello **world**"
	blocks2 := parseMarkdown(full)
	rendered.Reset()
	for _, block := range blocks2 {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					chunk := chunk
					rendered.WriteString(chunk.Text)
					rendered.WriteString("\n")
				}
			}
		}
	}
	out2 := rendered.String()
	if !strings.Contains(out2, "Hello") {
		t.Errorf("MISSING after close: %q", "Hello")
	}
	if !strings.Contains(out2, "world") {
		t.Errorf("MISSING after close: %q", "world")
	}
	if strings.Contains(out2, "**") {
		t.Errorf("raw ** should not appear after closing bold")
	}
}

func TestMarkdownStreamingListItem(t *testing.T) {
	partial := "- item one\n- item tw"
	blocks := parseMarkdown(partial)
	if len(blocks) == 0 {
		t.Fatal("expected blocks for partial list")
	}
	var rendered strings.Builder
	for _, block := range blocks {
		if comp, ok := blockComponentMap[block.Type]; ok {
			chunks := comp.Render(block, false)
			for _, c := range chunks {
				for _, chunk := range wordWrap(c.Text, 80, c.Style) {
					chunk := chunk
					rendered.WriteString(chunk.Text)
					rendered.WriteString("\n")
				}
			}
		}
	}
	out := rendered.String()
	if !strings.Contains(out, "item one") {
		t.Errorf("MISSING: %q", "item one")
	}
	if !strings.Contains(out, "item tw") {
		t.Errorf("MISSING: %q", "item tw")
	}
}

func TestMarkdownStreamingEmptyContent(t *testing.T) {
	blocks := parseMarkdown("")
	if len(blocks) != 0 {
		t.Errorf("expected 0 blocks for empty content, got %d", len(blocks))
	}
}

func TestToolCallRenderInAssistant(t *testing.T) {
	tc := ToolCallComponent{}
	chunks := tc.Render(chatMessage{
		ToolCalls: []ToolCallInfo{
			{Name: "read_file", Arg: "main.go"},
		},
	}, false)
	if len(chunks) < 3 {
		t.Fatal("expected at least 2 chunks (header + tool)")
	}
	header := chunks[1].Text
	if !strings.Contains(header, "Calling tools") {
		t.Errorf("header missing 'Calling tools': %q", header)
	}
	toolLine := chunks[2].Text
	if !strings.Contains(toolLine, "read_file: main.go") {
		t.Errorf("tool line missing name: %q", toolLine)
	}
	if !strings.Contains(toolLine, "main.go") {
		t.Errorf("tool line missing arg: %q", toolLine)
	}
}

func TestToolCallMsgAppendToMessage(t *testing.T) {
	m := testModelWithMessages([]chatMessage{
		{Role: "assistant", Content: "", Streaming: true},
	})
	m.curAssistant = &m.messages[len(m.messages)-1]

	// Send ToolCallMsg
	model, cmd := m.Update(ToolCallMsg{
		MsgIdx: -1,
		Name:   "read_file",
		Arg:    "main.go",
	})
	m = model.(*TuiModel)
	if cmd == nil {
		t.Fatal("expected a command (waitForStream)")
	}

	// Verify tool call was appended
	if len(m.messages[0].ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(m.messages[0].ToolCalls))
	}
	if m.messages[0].ToolCalls[0].Name != "read_file" {
		t.Errorf("expected name 'read_file', got %q", m.messages[0].ToolCalls[0].Name)
	}
	if m.messages[0].ToolCalls[0].Arg != "main.go" {
		t.Errorf("expected arg 'main.go', got %q", m.messages[0].ToolCalls[0].Arg)
	}

	// Send a second tool call
	m.Update(ToolCallMsg{Name: "search_files", Arg: "pattern: parseMarkdown"})
	if len(m.messages[0].ToolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(m.messages[0].ToolCalls))
	}
	if m.messages[0].ToolCalls[1].Name != "search_files" {
		t.Errorf("expected name 'search_files', got %q", m.messages[0].ToolCalls[1].Name)
	}

	// Render the full assistant component and verify tool calls appear
	chunks := AssistantComponent{}.Render(m.messages[0], false)
	var rendered strings.Builder
	for _, c := range chunks {
		rendered.WriteString(c.Text)
		rendered.WriteByte('\n')
	}
	out := rendered.String()
	if !strings.Contains(out, "Calling tools") {
		t.Errorf("missing 'Calling tools' header")
	}
	if !strings.Contains(out, "read_file") {
		t.Errorf("missing 'read_file'")
	}
	if !strings.Contains(out, "search_files") {
		t.Errorf("missing 'search_files'")
	}
}

func TestToolCallFlowFull(t *testing.T) {
	m := testModelWithMessages([]chatMessage{
		{Role: "assistant", Content: "", Streaming: true},
	})
	m.curAssistant = &m.messages[len(m.messages)-1]

	// Simulate: reasoning → tool call → text delta → stream done
	m.Update(StreamMsg{ReasoningDelta: "Let me check the codebase."})
	m.Update(ToolCallMsg{Name: "read_file", Arg: "main.go"})
	m.Update(ToolResultMsg{})
	m.Update(StreamMsg{TextDelta: "Found it!"})
	m.Update(StreamDone{Content: "Found it!", Error: nil})

	// Verify final state
	if len(m.messages[0].ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(m.messages[0].ToolCalls))
	}
	if m.messages[0].Content != "Found it!" {
		t.Errorf("expected content 'Found it!', got %q", m.messages[0].Content)
	}
	if m.messages[0].ReasoningContent != "Let me check the codebase." {
		t.Errorf("unexpected reasoning: %q", m.messages[0].ReasoningContent)
	}

	// Render and verify all sections appear
	chunks := AssistantComponent{}.Render(m.messages[0], false)
	var rendered strings.Builder
	for _, c := range chunks {
		rendered.WriteString(c.Text)
		rendered.WriteByte('\n')
	}
	out := rendered.String()

	if !strings.Contains(out, "Let me check") {
		t.Errorf("missing reasoning in render")
	}
	if !strings.Contains(out, "Calling tools") {
		t.Errorf("missing tool call header in render")
	}
	if !strings.Contains(out, "read_file") {
		t.Errorf("missing tool call name in render")
	}
	if !strings.Contains(out, "Response:") {
		t.Errorf("missing Response: label")
	}
}
