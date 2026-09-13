package lsp

import (
	"context"
	"testing"
)

// TestOptionalIntArg covers the tolerant argument reader used by the LSP tools.
func TestOptionalIntArg(t *testing.T) {
	args := map[string]any{
		"f":   3.0,
		"i":   4,
		"i64": int64(5),
		"s":   "6",
		"b":   true,
	}
	cases := []struct {
		key   string
		want  int
		wantK bool
	}{
		{"f", 3, true},
		{"i", 4, true},
		{"i64", 5, true},
		{"s", 0, false},
		{"b", 0, false},
		{"missing", 0, false},
	}
	for _, tc := range cases {
		got, ok := optionalIntArg(args, tc.key)
		if ok != tc.wantK || got != tc.want {
			t.Errorf("optionalIntArg(%q) = (%d, %v), want (%d, %v)", tc.key, got, ok, tc.want, tc.wantK)
		}
	}
}

// TestPositionToolsRejectMissingArgs guards against the unchecked type
// assertions that used to panic on a schema-legal call.
func TestPositionToolsRejectMissingArgs(t *testing.T) {
	for _, tt := range []ToolType{ToolGoToDefinition, ToolFindReferences, ToolHover} {
		tool := ToolFactory(tt)

		if _, err := tool.Execute(context.Background(), map[string]any{"file_path": "x.go"}); err == nil {
			t.Errorf("%s: expected an error when line/character are missing", tt)
		}

		if _, err := tool.Execute(context.Background(), map[string]any{
			"file_path": "x.go",
			"line":      "3",
			"character": true,
		}); err == nil {
			t.Errorf("%s: expected an error for malformed line/character", tt)
		}
	}
}
