package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/yusiwen/tinycode/lsp"
)

type editOp struct {
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// Edit returns a Tool that performs search/replace edits on a file.
// The LLM provides the exact text to find and replace, ensuring
// precision without needing to specify line numbers or rewrite
// the entire file.
func Edit() Tool {
	return Tool{
		Name:        "edit",
		Description: "Apply search/replace edits to a file. " +
			"Provide old_string (exact text to find) and new_string (replacement). " +
			"If old_string appears more than once, provide surrounding context to disambiguate. " +
			"Multiple edits in one call are applied in order.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]any{
					"type":        "string",
					"description": "Absolute or relative path to the file to edit",
				},
				"edits": map[string]any{
					"type":        "array",
					"description": "List of search/replace operations to apply in order",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"old_string": map[string]any{
								"type":        "string",
								"description": "Exact text to find (must appear exactly once in the file)",
							},
							"new_string": map[string]any{
								"type":        "string",
								"description": "Text to replace old_string with",
							},
						},
					},
				},
			},
			"required": []string{"path", "edits"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			path, _ := args["path"].(string)
			if path == "" {
				return "", fmt.Errorf("path is required")
			}

			// Layer 2: Path restriction check
			if err := DefaultSandbox.CheckPath(path); err != nil {
				if ad, ok := err.(*AccessDenied); ok {
					return ad.DenyHint(), nil
				}
				return "", fmt.Errorf("path check: %w", err)
			}

			raw, ok := args["edits"]
			if !ok {
				return "", fmt.Errorf("edits is required")
			}
			b, err := json.Marshal(raw)
			if err != nil {
				return "", fmt.Errorf("parse edits: %w", err)
			}
			var edits []editOp
			if err := json.Unmarshal(b, &edits); err != nil {
				return "", fmt.Errorf("unmarshal edits: %w", err)
			}
			if len(edits) == 0 {
				return "", fmt.Errorf("at least one edit is required")
			}

			// LSP baseline BEFORE edits
			if lsp.IsAvailable() {
				lsp.SnapshotBaseline(path)
			}

			// Read the file
			data, err := os.ReadFile(path)
			if err != nil {
				return "", fmt.Errorf("read %s: %w", path, err)
			}
			content := string(data)

			applied := 0
			totalChanges := 0
			for _, edit := range edits {
				if edit.OldString == "" {
					return "", fmt.Errorf("old_string is required for edit %d", applied+1)
				}

				// Count occurrences
				count := strings.Count(content, edit.OldString)
				if count == 0 {
					return "", fmt.Errorf("edit %d: old_string not found in file", applied+1)
				}
				if count > 1 {
					return "", fmt.Errorf(
						"edit %d: old_string appears %d times — provide more surrounding context to disambiguate",
						applied+1, count)
				}

				content = strings.Replace(content, edit.OldString, edit.NewString, 1)
				applied++
				totalChanges += strings.Count(edit.NewString, "\n") + 1
			}

			// Write back
			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return "", fmt.Errorf("write %s: %w", path, err)
			}

			result := fmt.Sprintf("Applied %d edit(s) to %s (%d line(s) changed)", applied, path, totalChanges)

			// LSP diagnostics: only new errors
			if lsp.IsAvailable() {
				if newDiags := lsp.GetNewDiagnostics(path); len(newDiags) > 0 {
					result += lsp.FormatDiagnostics(path, newDiags)
				}
			}

			return result, nil
		},
	}
}
