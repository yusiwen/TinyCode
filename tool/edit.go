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

// fuzzyResult holds the result of a fuzzy find operation.
type fuzzyResult struct {
	matchText string // the actual text in the file that matched
	count     int    // number of matches (0 = not found, 1 = unique, >1 = ambiguous)
	strategy  string // name of the strategy that found the match
}

// Edit returns a Tool that performs search/replace edits on a file.
// The LLM provides the exact text to find and replace, ensuring
// precision without needing to specify line numbers or rewrite
// the entire file. Fuzzy matching is attempted when exact match fails.
func Edit() Tool {
	return Tool{
		Name:        "edit",
		Description: "Apply search/replace edits to a file. " +
			"Provide old_string (exact text to find) and new_string (replacement). " +
			"If old_string appears more than once, provide surrounding context. " +
			"Fuzzy matching is used as fallback.",
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
								"description": "Text to find (exact or fuzzy matched)",
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

			if lsp.IsAvailable() {
				lsp.SnapshotBaseline(path)
			}

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

				// Fuzzy find: try strategies in order
				result := fuzzyFind(content, edit.OldString)
				if result.count == 0 {
					return "", fmt.Errorf("edit %d: old_string not found in file (tried %d strategies)", applied+1, len(strategies))
				}
				if result.count > 1 {
					return "", fmt.Errorf(
						"edit %d: old_string appears %d times (strategy: %s) — provide more surrounding context",
						applied+1, result.count, result.strategy)
				}

				// Correct indentation: adjust new_string to match original indent
				replacement := correctIndentation(result.matchText, edit.NewString)

				content = strings.Replace(content, result.matchText, replacement, 1)
				applied++
				totalChanges += strings.Count(replacement, "\n") + 1
			}

			if err := os.WriteFile(path, []byte(content), 0644); err != nil {
				return "", fmt.Errorf("write %s: %w", path, err)
			}

			result := fmt.Sprintf("Applied %d edit(s) to %s (%d line(s) changed)", applied, path, totalChanges)

			if lsp.IsAvailable() {
				if newDiags := lsp.GetNewDiagnostics(path); len(newDiags) > 0 {
					result += lsp.FormatDiagnostics(path, newDiags)
				}
			}

			return result, nil
		},
	}
}

// ── Fuzzy matching strategies ──

type strategyFunc func(content, search string) *fuzzyResult

var strategies = []strategyFunc{
	tryExact,
	tryLineTrimmed,
	tryWhitespaceNormalized,
	tryIndentationFlexible,
	tryEscapeNormalized,
	tryUnicodeNormalized,
	tryBlockAnchor,
}

func fuzzyFind(content, search string) *fuzzyResult {
	for _, fn := range strategies {
		if r := fn(content, search); r != nil {
			return r
		}
	}
	return &fuzzyResult{count: 0}
}

// 1. Exact match
func tryExact(content, search string) *fuzzyResult {
	count := strings.Count(content, search)
	if count == 0 {
		return nil
	}
	return &fuzzyResult{matchText: search, count: count, strategy: "exact"}
}

// 2. Line-trimmed: strip leading/trailing whitespace per line
func tryLineTrimmed(content, search string) *fuzzyResult {
	normalized := trimLines(search)
	return findUnique(content, search, normalized, func(s string) string {
		return trimLines(s)
	}, "line-trimmed")
}

func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		// Strip trailing spaces but preserve leading spaces for indentation
		lines[i] = strings.TrimRight(line, " \t\r")
	}
	return strings.Join(lines, "\n")
}

// 3. Whitespace normalized: collapse multiple spaces/tabs to single space
func tryWhitespaceNormalized(content, search string) *fuzzyResult {
	normalized := collapseWS(search)
	return findUnique(content, search, normalized, func(s string) string {
		return collapseWS(s)
	}, "ws-normalized")
}

func collapseWS(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		in := strings.Fields(line)
		lines[i] = strings.Join(in, " ")
	}
	return strings.Join(lines, "\n")
}

// 4. Indentation flexible: strip common leading whitespace
func tryIndentationFlexible(content, search string) *fuzzyResult {
	normalized := stripCommonIndent(search)
	return findUnique(content, search, normalized, func(s string) string {
		return stripCommonIndent(s)
	}, "indent-flexible")
}

func stripCommonIndent(s string) string {
	lines := strings.Split(s, "\n")
	minIndent := -1
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		indent := len(line) - len(trimmed)
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	}
	if minIndent <= 0 {
		return s
	}
	for i, line := range lines {
		if len(line) >= minIndent {
			lines[i] = line[minIndent:]
		}
	}
	return strings.Join(lines, "\n")
}

// 5. Escape normalized: convert \n literals to actual newlines
func tryEscapeNormalized(content, search string) *fuzzyResult {
	converted := unescape(search)
	return findUnique(content, search, converted, func(s string) string {
		return unescape(s)
	}, "escape-normalized")
}

func unescape(s string) string {
	s = strings.ReplaceAll(s, "\\n", "\n")
	s = strings.ReplaceAll(s, "\\t", "\t")
	s = strings.ReplaceAll(s, "\\\"", "\"")
	s = strings.ReplaceAll(s, "\\'", "'")
	s = strings.ReplaceAll(s, "\\\\", "\\")
	return s
}

// 6. Unicode normalized: smart quotes → ASCII, em dashes → --, etc.
func tryUnicodeNormalized(content, search string) *fuzzyResult {
	normalized := normalizeUnicode(search)
	return findUnique(content, search, normalized, func(s string) string {
		return normalizeUnicode(s)
	}, "unicode-normalized")
}

func normalizeUnicode(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\u201C', '\u201D':
			b.WriteRune('"')
		case '\u2018', '\u2019':
			b.WriteRune('\'')
		case '\u2013', '\u2014':
			b.WriteString("--")
		case '\u2026':
			b.WriteString("...")
		case '\u00A0':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// 7. Block anchor: find a block with matching first/last lines, then check
// Levenshtein similarity of the trimmed content.
func tryBlockAnchor(content, search string) *fuzzyResult {
	searchTrimmed := strings.TrimSpace(search)
	searchLines := strings.Split(searchTrimmed, "\n")
	if len(searchLines) < 3 {
		return nil
	}
	firstLine := strings.TrimSpace(searchLines[0])
	lastLine := strings.TrimSpace(searchLines[len(searchLines)-1])
	if firstLine == "" || lastLine == "" {
		return nil
	}

	contentLines := strings.Split(content, "\n")
	var matches []string

	for i := 0; i < len(contentLines); i++ {
		if strings.TrimSpace(contentLines[i]) != firstLine {
			continue
		}
		// Find the matching last line somewhere after first
		for j := i + 1; j < len(contentLines); j++ {
			if strings.TrimSpace(contentLines[j]) != lastLine {
				continue
			}
			candidate := strings.Join(contentLines[i:j+1], "\n")
			if levenshteinSimilarity(candidate, search) >= 0.65 {
				matches = append(matches, candidate)
				break // only one candidate per start position
			}
		}
	}

	if len(matches) == 0 {
		return nil
	}
	return &fuzzyResult{
		matchText: matches[0],
		count:     len(matches),
		strategy:  "block-anchor",
	}
}

// findUnique is a helper: tries to locate a unique occurrence of `search`
// by normalizing both content and search via a transform function.
func findUnique(content, search, normalizedSearch string,
	transform func(string) string, strategy string) *fuzzyResult {

	if search == normalizedSearch && transform(content) == content {
		return nil // neither search nor content needs this transform
	}
	normalizedContent := transform(content)
	count := strings.Count(normalizedContent, normalizedSearch)
	if count == 0 {
		return nil
	}
	if count > 1 {
		return &fuzzyResult{count: count, strategy: strategy}
	}
	// Unique match found — locate the original text in content
	idx := strings.Index(normalizedContent, normalizedSearch)
	end := idx + len(normalizedSearch)
	// Map back to original content
	matchText := content[idx:end]
	return &fuzzyResult{matchText: matchText, count: 1, strategy: strategy}
}

// ── Indentation correction ──

// correctIndentation adjusts new_string's indentation to match old_match's style.
func correctIndentation(oldMatch, newString string) string {
	oldLines := strings.Split(oldMatch, "\n")
	newLines := strings.Split(newString, "\n")
	if len(oldLines) == 0 || len(newLines) == 0 {
		return newString
	}
	// First line: compute leading whitespace from oldMatch, preserve exact
	oldFirstWS := leadingWhitespace(oldLines[0])
	newFirstWS := leadingWhitespace(newLines[0])
	if newFirstWS == oldFirstWS || oldFirstWS == "" {
		return newString
	}
	// Replace new_string's first-line indent with old_match's
	for i := range newLines {
		if newLines[i] == "" {
			continue
		}
		if i == 0 {
			// First line: use old's exact leading whitespace
			content := strings.TrimLeft(newLines[i], " \t")
			newLines[i] = oldFirstWS + content
		} else if strings.TrimLeft(newLines[i], " \t") != "" {
			// Subsequent non-empty lines: adjust relative indent
			content := strings.TrimLeft(newLines[i], " \t")
			// Compute the relative indent level and add it to oldFirstWS
			extraIndent := len(newLines[i]) - len(content) - len(newFirstWS)
			if extraIndent < 0 {
				extraIndent = 0
			}
			newLines[i] = oldFirstWS + strings.Repeat(" ", extraIndent) + content
		}
	}
	return strings.Join(newLines, "\n")
}

func leadingWhitespace(s string) string {
	for i, r := range s {
		if r != ' ' && r != '\t' {
			return s[:i]
		}
	}
	return s
}

// ── Levenshtein similarity ──

func levenshteinSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	distance := levenshteinDistance(a, b)
	maxLen := len(a)
	if len(b) > maxLen {
		maxLen = len(b)
	}
	if maxLen == 0 {
		return 1.0
	}
	return 1.0 - float64(distance)/float64(maxLen)
}

func levenshteinDistance(a, b string) int {
	la, lb := len(a), len(b)
	if la == 0 {
		return lb
	}
	if lb == 0 {
		return la
	}
	// Use single-row optimization
	prev := make([]int, lb+1)
	for j := 0; j <= lb; j++ {
		prev[j] = j
	}
	for i := 1; i <= la; i++ {
		curr := make([]int, lb+1)
		curr[0] = i
		for j := 1; j <= lb; j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			curr[j] = min(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = curr
	}
	return prev[lb]
}

func min(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
