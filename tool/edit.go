package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

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
	start     int    // byte offset of matchText in the content (unique matches)
	end       int    // byte offset just past matchText in the content
}

// Edit returns a Tool that performs search/replace edits on a file.
// The LLM provides the exact text to find and replace, ensuring
// precision without needing to specify line numbers or rewrite
// the entire file. Fuzzy matching is attempted when exact match fails.
func Edit() Tool {
	return Tool{
		Name: "edit",
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

			// All I/O uses the resolved path returned by the gate.
			safePath, denied, err := CheckPathAccess(ctx, path)
			if err != nil {
				return "", err
			}
			if denied != "" {
				return denied, nil
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
				lsp.SnapshotBaseline(safePath)
			}

			data, err := readSandboxed(safePath)
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
					errStr := fmt.Sprintf("edit %d: old_string not found in file (tried %d strategies) — "+
						"the file may have been modified since you last read it. "+
						"Try read_file first, then edit again.", applied+1, len(strategies))
					if applied > 0 {
						return fmt.Sprintf("Partial success: %d edit(s) applied before error.\n%s", applied, errStr), nil
					}
					return "", fmt.Errorf("%s", errStr)
				}
				if result.count > 1 {
					if applied > 0 {
						return fmt.Sprintf("Partial success: %d edit(s) applied before error.\nEdit %d: old_string appears %d times in the file. Provide more context to make it unique (include surrounding lines). Matched via strategy: %s",
							applied, applied+1, result.count, result.strategy), nil
					}
					return "", fmt.Errorf(
						"edit %d: old_string appears %d times in the file. Provide more context to make it unique (include surrounding lines). Matched via strategy: %s",
						applied+1, result.count, result.strategy)
				}

				// Correct indentation: adjust new_string to match original indent
				replacement := correctIndentation(result.matchText, edit.NewString)
				// A fuzzy match can start after the line's indentation (the
				// strategy stripped it); re-apply the file's own indentation so
				// the replacement block lines up.
				replacement = matchLineIndent(content, result, replacement)

				// Replace exactly the matched range: strings.Replace would hit
				// the first occurrence of the text, which need not be the match
				// the strategy selected.
				if result.start < 0 || result.end > len(content) || result.start >= result.end {
					return "", fmt.Errorf("edit %d: internal error: invalid match range [%d,%d) for %q",
						applied+1, result.start, result.end, result.strategy)
				}
				content = content[:result.start] + replacement + content[result.end:]
				applied++
				totalChanges += strings.Count(replacement, "\n") + 1
			}

			if err := writeSandboxed(safePath, []byte(content), 0644); err != nil {
				return "", fmt.Errorf("write %s: %w", path, err)
			}

			result := fmt.Sprintf("Applied %d edit(s) to %s (%d line(s) changed)", applied, path, totalChanges)

			if lsp.IsAvailable() {
				if newDiags := lsp.GetNewDiagnostics(safePath); len(newDiags) > 0 {
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
		r := fn(content, search)
		if r == nil {
			continue
		}
		// A byte-oriented strategy can cut a multi-byte character in half when
		// the search text is not valid UTF-8 (a lone lead byte, a truncated
		// sequence). Replacing that reported range would splice those bytes and
		// leave the file with a broken encoding, so the match is refused and the
		// remaining strategies get their turn.
		if r.count == 1 && !runeAligned(content, r.start, r.end) {
			continue
		}
		return r
	}
	return &fuzzyResult{count: 0}
}

// runeAligned reports whether the byte range [start,end) of content lies on
// UTF-8 character boundaries. Content that is not valid UTF-8 has no boundaries
// to honour, and an out-of-range span is never aligned.
func runeAligned(content string, start, end int) bool {
	if start < 0 || end < start || end > len(content) {
		return false
	}
	if !utf8.ValidString(content) {
		return true
	}
	if start > 0 && !utf8.RuneStart(content[start]) {
		return false
	}
	return end == len(content) || utf8.RuneStart(content[end])
}

// 1. Exact match
func tryExact(content, search string) *fuzzyResult {
	count := strings.Count(content, search)
	if count == 0 {
		return nil
	}
	idx := strings.Index(content, search)
	return &fuzzyResult{
		matchText: search, count: count, strategy: "exact",
		start: idx, end: idx + len(search),
	}
}

// 2. Line-trimmed: strip leading/trailing whitespace per line
func tryLineTrimmed(content, search string) *fuzzyResult {
	return findUnique(content, search, trimLines(search), mapTrimLines, "line-trimmed")
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
	return findUnique(content, search, collapseWS(search), mapCollapseWS, "ws-normalized")
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
	return findUnique(content, search, stripCommonIndent(search), mapStripCommonIndent, "indent-flexible")
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
	return findUnique(content, search, unescape(search), mapUnescape, "escape-normalized")
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
	return findUnique(content, search, normalizeUnicode(search), mapNormalizeUnicode, "unicode-normalized")
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
	// Byte offset of the first character of each line.
	lineStart := make([]int, len(contentLines))
	off := 0
	for i, line := range contentLines {
		lineStart[i] = off
		off += len(line) + 1 // +1 for the newline that was split away
	}

	var matches []string
	firstStart, firstEnd := -1, -1

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
				if firstStart < 0 {
					firstStart = lineStart[i]
					firstEnd = lineStart[j] + len(contentLines[j])
				}
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
		start:     firstStart,
		end:       firstEnd,
	}
}

// ── Offset-preserving normalization ──
//
// Fuzzy strategies compare normalized text, but the edit has to replace the
// ORIGINAL bytes. Slicing the original with offsets computed on the normalized
// string (what this code used to do) silently replaces the wrong region
// whenever a transform changes the text length. The mapped transforms below
// record, for every normalized byte, the range it came from in the original.

// normSpan is the source byte range of one normalized byte.
type normSpan struct{ start, end int }

// mappedTransform is normalized text plus the source range of each byte.
type mappedTransform struct {
	text  string
	spans []normSpan
}

// sourceRange maps a normalized byte range back to the original byte range.
func (m mappedTransform) sourceRange(start, end int) (int, int) {
	if start < 0 || start >= end || end > len(m.spans) {
		return 0, 0
	}
	return m.spans[start].start, m.spans[end-1].end
}

// mappedBuilder accumulates normalized bytes and their source ranges.
type mappedBuilder struct {
	b     strings.Builder
	spans []normSpan
}

// emit appends replacement text mapped to the original range [start,end).
func (m *mappedBuilder) emit(text string, start, end int) {
	for i := 0; i < len(text); i++ {
		m.b.WriteByte(text[i])
		m.spans = append(m.spans, normSpan{start, end})
	}
}

// copyRange appends src[start:end] with an identity mapping.
func (m *mappedBuilder) copyRange(src string, start, end int) {
	for i := start; i < end; i++ {
		m.b.WriteByte(src[i])
		m.spans = append(m.spans, normSpan{i, i + 1})
	}
}

func (m *mappedBuilder) result() mappedTransform {
	return mappedTransform{text: m.b.String(), spans: m.spans}
}

// eachLine walks src line by line (keeping the newline with the line it ends).
func eachLine(src string, fn func(start, end int, line string)) {
	for i := 0; i < len(src); {
		j := strings.IndexByte(src[i:], '\n')
		if j < 0 {
			fn(i, len(src), src[i:])
			return
		}
		fn(i, i+j+1, src[i:i+j+1])
		i += j + 1
	}
}

// mapTrimLines mirrors trimLines with source tracking.
func mapTrimLines(src string) mappedTransform {
	var mb mappedBuilder
	eachLine(src, func(start, end int, line string) {
		body := strings.TrimSuffix(line, "\n")
		trimmed := strings.TrimRight(body, " \t\r")
		mb.copyRange(src, start, start+len(trimmed))
		if len(body) < len(line) { // emit the newline we stripped
			mb.copyRange(src, end-1, end)
		}
	})
	return mb.result()
}

// mapCollapseWS mirrors collapseWS (strings.Fields joined by one space).
func mapCollapseWS(src string) mappedTransform {
	var mb mappedBuilder
	eachLine(src, func(start, end int, line string) {
		body := strings.TrimSuffix(line, "\n")
		i := 0
		first := true
		for i < len(body) {
			for i < len(body) && isSpaceByte(body[i]) {
				i++
			}
			if i >= len(body) {
				break
			}
			segStart := i
			for i < len(body) && !isSpaceByte(body[i]) {
				i++
			}
			if !first {
				// The separating space is attributed to the whitespace run
				// that preceded this segment.
				mb.emit(" ", segStart-1, segStart)
			}
			mb.copyRange(src, start+segStart, start+i)
			first = false
		}
		if len(body) < len(line) {
			mb.copyRange(src, end-1, end)
		}
	})
	return mb.result()
}

func isSpaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\v', '\f', '\r':
		return true
	}
	return false
}

// mapStripCommonIndent mirrors stripCommonIndent with source tracking.
func mapStripCommonIndent(src string) mappedTransform {
	minIndent := -1
	eachLine(src, func(_, _ int, line string) {
		body := strings.TrimSuffix(line, "\n")
		trimmed := strings.TrimLeft(body, " \t")
		if trimmed == "" {
			return
		}
		indent := len(body) - len(trimmed)
		if minIndent < 0 || indent < minIndent {
			minIndent = indent
		}
	})
	var mb mappedBuilder
	eachLine(src, func(start, end int, line string) {
		body := strings.TrimSuffix(line, "\n")
		cut := 0
		if minIndent > 0 && len(body) >= minIndent {
			cut = minIndent
		}
		mb.copyRange(src, start+cut, start+len(body))
		if len(body) < len(line) {
			mb.copyRange(src, end-1, end)
		}
	})
	return mb.result()
}

// mapUnescape mirrors unescape with source tracking. Escapes are consumed left
// to right, so a normalized byte maps to the whole escape sequence it replaced.
func mapUnescape(src string) mappedTransform {
	var mb mappedBuilder
	for i := 0; i < len(src); i++ {
		if src[i] == '\\' && i+1 < len(src) {
			var repl string
			switch src[i+1] {
			case 'n':
				repl = "\n"
			case 't':
				repl = "\t"
			case '"':
				repl = "\""
			case '\'':
				repl = "'"
			case '\\':
				repl = "\\"
			}
			if repl != "" {
				mb.emit(repl, i, i+2)
				i++
				continue
			}
		}
		mb.copyRange(src, i, i+1)
	}
	return mb.result()
}

// mapNormalizeUnicode mirrors normalizeUnicode with source tracking.
func mapNormalizeUnicode(src string) mappedTransform {
	var mb mappedBuilder
	for i, r := range src {
		// i is the byte offset of r; size is needed for the source range.
		size := len(string(r))
		var repl string
		switch r {
		case '\u201C', '\u201D':
			repl = "\""
		case '\u2018', '\u2019':
			repl = "'"
		case '\u2013', '\u2014':
			repl = "--"
		case '\u2026':
			repl = "..."
		case '\u00A0':
			repl = " "
		default:
			repl = string(r)
		}
		mb.emit(repl, i, i+size)
	}
	return mb.result()
}

// findUnique locates a unique occurrence of `search` in content after applying
// a normalization, and returns the ORIGINAL text that matched.
func findUnique(content, search, normalizedSearch string,
	mapped func(string) mappedTransform, strategy string) *fuzzyResult {

	mc := mapped(content)
	if search == normalizedSearch && mc.text == content {
		return nil // neither search nor content needs this transform
	}
	count := strings.Count(mc.text, normalizedSearch)
	if count == 0 {
		return nil
	}
	if count > 1 {
		return &fuzzyResult{count: count, strategy: strategy}
	}
	idx := strings.Index(mc.text, normalizedSearch)
	start, end := mc.sourceRange(idx, idx+len(normalizedSearch))
	if start < 0 || end > len(content) || start >= end {
		return nil
	}
	matchText := content[start:end]
	if !strings.Contains(content, matchText) {
		return nil
	}
	return &fuzzyResult{matchText: matchText, count: 1, strategy: strategy, start: start, end: end}
}

// matchLineIndent re-indents replacement when the match begins at the first
// non-whitespace character of an indented line but the replacement's first line
// carries no indentation of its own (typical for the dedenting fuzzy
// strategies). Otherwise the replacement is returned unchanged.
func matchLineIndent(content string, result *fuzzyResult, replacement string) string {
	if result.start <= 0 {
		return replacement
	}
	lineStart := strings.LastIndexByte(content[:result.start], '\n') + 1
	indent := content[lineStart:result.start]
	if indent == "" || strings.TrimLeft(indent, " \t") != "" {
		return replacement // match does not start at the line's content
	}
	if leadingWhitespace(replacement) != "" {
		return replacement // the replacement already brings its own indent
	}
	// The first line is left alone: the content prefix content[:start] already
	// carries the original indentation. The remaining lines were written at the
	// dedented level, so they need the indent added back.
	lines := strings.Split(replacement, "\n")
	for i := 1; i < len(lines); i++ {
		if lines[i] == "" {
			continue
		}
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
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
