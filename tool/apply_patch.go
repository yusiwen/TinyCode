package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yusiwen/tinycode/lsp"
)

type patchOpType int

const (
	opUpdate patchOpType = iota
	opAdd
	opDelete
)

type patchChunk struct {
	context string // context line (prefix " ")
	oldLine string // line to remove (prefix "-")
	newLine string // line to add (prefix "+")
}

type patchOp struct {
	typ    patchOpType
	path   string
	chunks []patchChunk
	newSrc string // for add ops: full content
}

// ApplyPatch returns a Tool that applies V4A format patches.
func ApplyPatch() Tool {
	return Tool{
		Name:        "apply_patch",
		Description: "Apply a V4A-format patch to one or more files. " +
			"Supports UPDATE (modify), ADD (create), and DELETE operations. " +
			"All operations are validated before any writes. " +
			"Format: *** Begin Patch / *** Update File: / *** Add File: / *** Delete File: / *** End Patch",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"patch_text": map[string]any{
					"type":        "string",
					"description": "The V4A format patch text containing all file operations",
				},
			},
			"required": []string{"patch_text"},
		},
		Execute: func(ctx context.Context, args map[string]any) (string, error) {
			patchText, _ := args["patch_text"].(string)
			if patchText == "" {
				return "", fmt.Errorf("patch_text is required")
			}

			// Phase 1: Parse
			ops, err := parseV4A(patchText)
			if err != nil {
				return "", fmt.Errorf("parse: %w", err)
			}
			if len(ops) == 0 {
				return "", fmt.Errorf("no operations found in patch")
			}

			// Phase 2: Validate all operations
			for _, op := range ops {
				switch op.typ {
				case opUpdate:
					data, err := os.ReadFile(op.path)
					if err != nil {
						return "", fmt.Errorf("validate %s: %w", op.path, err)
					}
					content := string(data)

					// Track replacements in reverse to avoid offset issues
					// Apply validation by checking each chunk can be found
					remaining := content
					for ci, chunk := range op.chunks {
						idx := strings.Index(remaining, chunk.oldLine)
						if idx < 0 {
							return "", fmt.Errorf(
								"validate %s: chunk %d: old line %q not found in file%s",
								op.path, ci+1, chunk.oldLine, formatFilePreview(content, chunk.oldLine))
						}
						remaining = remaining[idx+len(chunk.oldLine):]
						// Find the newline after this match for next iteration
						nlIdx := strings.Index(remaining, "\n")
						if nlIdx >= 0 {
							remaining = remaining[nlIdx:]
						}
					}
				case opAdd:
					if err := os.MkdirAll(filepath.Dir(op.path), 0755); err != nil {
						return "", fmt.Errorf("validate %s: mkdir: %w", op.path, err)
					}
				case opDelete:
					if _, err := os.Stat(op.path); os.IsNotExist(err) {
						return "", fmt.Errorf("validate %s: file not found", op.path)
					}
				}
			}

			// Phase 3: Apply all operations
			type opResult struct {
				path    string
				applied string // "updated", "created", "deleted"
				lines   int
			}
			var results []opResult

			for _, op := range ops {
				switch op.typ {
				case opUpdate:
					// LSP baseline before write
					if lsp.IsAvailable() {
						lsp.SnapshotBaseline(op.path)
					}

					data, _ := os.ReadFile(op.path)
					content := string(data)
					lines := 0
					for _, chunk := range op.chunks {
						content = strings.Replace(content, chunk.oldLine, chunk.newLine, 1)
						lines += strings.Count(chunk.newLine, "\n") + 1
					}
					if err := os.WriteFile(op.path, []byte(content), 0644); err != nil {
						// Partial failure — report what succeeded so far
						return fmt.Sprintf("Partial failure after updating %d file(s): %v",
							len(results), err), nil
					}
					results = append(results, opResult{path: op.path, applied: "updated", lines: lines})

				case opAdd:
					if err := os.MkdirAll(filepath.Dir(op.path), 0755); err != nil {
						return fmt.Sprintf("Partial failure after %d ops: %v", len(results), err), nil
					}
					if err := os.WriteFile(op.path, []byte(op.newSrc), 0644); err != nil {
						return fmt.Sprintf("Partial failure after %d ops: %v", len(results), err), nil
					}
					results = append(results, opResult{path: op.path, applied: "created", lines: strings.Count(op.newSrc, "\n") + 1})

				case opDelete:
					if err := os.Remove(op.path); err != nil {
						return fmt.Sprintf("Partial failure after %d ops: %v", len(results), err), nil
					}
					results = append(results, opResult{path: op.path, applied: "deleted", lines: 0})
				}
			}

			// Build summary
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("Applied patch: %d operation(s)\n", len(results)))
			for _, r := range results {
				switch r.applied {
				case "updated":
					sb.WriteString(fmt.Sprintf("  U %s (%d lines)\n", r.path, r.lines))
				case "created":
					sb.WriteString(fmt.Sprintf("  A %s (%d lines)\n", r.path, r.lines))
				case "deleted":
					sb.WriteString(fmt.Sprintf("  D %s\n", r.path))
				}
			}

			// LSP diagnostics per updated file
			if lsp.IsAvailable() {
				for _, r := range results {
					if r.applied == "updated" {
						if diags := lsp.GetNewDiagnostics(r.path); len(diags) > 0 {
							sb.WriteString(lsp.FormatDiagnostics(r.path, diags))
						}
					}
				}
			}

			return strings.TrimSpace(sb.String()), nil
		},
	}
}

// parseV4A parses a V4A-format patch string into operations.
func parseV4A(patch string) ([]patchOp, error) {
	lines := strings.Split(patch, "\n")
	var ops []patchOp
	var current *patchOp
	inUpdate := false

	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r")

		// Strip heredoc indent (bash <<'EOF' style)
		cleaned := strings.TrimSpace(trimmed)

		switch {
		case strings.HasPrefix(cleaned, "*** Begin Patch"):
			// Start — clear any pending
			ops = nil
			current = nil
			inUpdate = false

		case strings.HasPrefix(cleaned, "*** Update File: "):
			path := strings.TrimPrefix(cleaned, "*** Update File: ")
			if current != nil && current.typ == opUpdate {
				ops = append(ops, *current)
			}
			current = &patchOp{typ: opUpdate, path: path}
			inUpdate = true

		case strings.HasPrefix(cleaned, "*** Add File: "):
			if current != nil {
				ops = append(ops, *current)
			}
			path := strings.TrimPrefix(cleaned, "*** Add File: ")
			current = &patchOp{typ: opAdd, path: path, newSrc: ""}
			inUpdate = false

		case strings.HasPrefix(cleaned, "*** Delete File: "):
			if current != nil {
				ops = append(ops, *current)
			}
			path := strings.TrimPrefix(cleaned, "*** Delete File: ")
			current = &patchOp{typ: opDelete, path: path}
			ops = append(ops, *current)
			current = nil
			inUpdate = false

		case strings.HasPrefix(cleaned, "*** End Patch"):
			if current != nil {
				ops = append(ops, *current)
			}
			current = nil
			inUpdate = false

		case strings.HasPrefix(cleaned, "@@"):
			// Hunk header — ignore, just marks position
			continue

		case inUpdate && current != nil && len(trimmed) > 0:
			prefix := trimmed[0]
			rest := ""
			if len(trimmed) > 1 {
				rest = trimmed[1:] // including any leading spaces
			}
			switch prefix {
			case ' ':
				// Context line (not used in hermetic matching)
				// We only track -/+ lines
			case '-':
				chunk := patchChunk{oldLine: rest + "\n"}
				// Look ahead for + line
				if i+1 < len(lines) {
					next := strings.TrimRight(lines[i+1], "\r")
					if len(next) > 0 && next[0] == '+' {
						if len(next) > 1 {
							chunk.newLine = next[1:] + "\n"
						} else {
							chunk.newLine = "\n"
						}
					}
				}
				current.chunks = append(current.chunks, chunk)
			case '+':
				// Only add if not paired with a preceding -
				// (paired + lines are handled by the - case above)
				if i == 0 || len(lines[i-1]) == 0 || lines[i-1][0] != '-' {
					// Standalone + line (for new content in non-update context)
					// In update context, this shouldn't happen
				}
			}
		case inUpdate && current != nil && len(trimmed) == 0:
			// Empty line could be significant (e.g., blank context line)
			continue

		default:
			if current != nil && current.typ == opAdd {
				// Collect content for new files
				if current.newSrc == "" {
					cleanedLine := line
					if len(line) > 0 && line[0] == '+' {
						cleanedLine = line[1:]
					}
					current.newSrc = cleanedLine + "\n"
				} else {
					cleanedLine := line
					if len(line) > 0 && line[0] == '+' {
						cleanedLine = line[1:]
					}
					current.newSrc += cleanedLine + "\n"
				}
			}
		}
	}

	// If we ended while in an operation, save it
	if current != nil {
		ops = append(ops, *current)
	}

	return ops, nil
}

// formatFilePreview creates a helpful error message showing context around a search string.
func formatFilePreview(content, search string) string {
	lines := strings.Split(content, "\n")
	searchLines := strings.Split(strings.TrimRight(search, "\n"), "\n")
	if len(searchLines) == 0 {
		return ""
	}
	firstLine := strings.TrimSpace(searchLines[0])
	for i, l := range lines {
		if strings.Contains(l, firstLine) {
			showFrom := i - 3
			if showFrom < 0 {
				showFrom = 0
			}
			showTo := i + 4
			if showTo > len(lines) {
				showTo = len(lines)
			}
			var sb strings.Builder
			sb.WriteString(fmt.Sprintf("\nNear line %d:\n", i+1))
			for j := showFrom; j < showTo; j++ {
				marker := " "
				if j == i {
					marker = ">"
				}
				sb.WriteString(fmt.Sprintf("  %s %5d| %s\n", marker, j+1, lines[j]))
			}
			return sb.String()
		}
	}
	return ""
}
