package tool

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// The two sides of the comparison, each read from its own source of truth.
//
// `toolNameLiteral` matches `Name: "…"` at the start of a line, which is how
// this package's tool factories declare their name. It deliberately does not
// match `Name: name` (the MCP tools, built at runtime from a server's tool list)
// nor `ServerName: cfg.Name` — an unanchored `Name:` pattern would match both.
var (
	toolNameLiteral = regexp.MustCompile(`(?m)^\s*Name:\s*"([a-z][a-z0-9_]*)"`)
	lspToolType     = regexp.MustCompile(`ToolType = "([a-z][a-z0-9_]*)"`)
	registryRow     = regexp.MustCompile("^\\| `([a-z][a-z0-9_]*|mcp_\\*)`")
)

// TestDocumentedToolRegistryMatchesTheCode holds CODEBASE.md's "Complete Tool
// Name Registry" table and the tool names the code defines together.
//
// That table is where a reader goes to learn what the built-in tools are, and it
// is written by hand. Issue #188 found it titled "Complete" while
// `web_extract_browser` was registered in `main.go` and missing from the table —
// the omission hidden by the row count still reading 24, because the dynamic
// `mcp_*` row was counted as one of the tools. A hand-written list beside a
// registry needs a check that reads both; this is that check.
//
// Both sides are derived from their source rather than restated here: the names
// from the `Name: "…"` literals this package defines plus the four `lsp` tool
// types, and the rows of that one table. `mcp_*` is the single row that is not a
// static name, since MCP servers are registered at runtime as
// `mcp_<server>_<tool>` and one row therefore stands for a family — so it is
// allowed on the document side only.
//
// This compares the definitions, not `main.go`'s registrations: the table
// describes the package's tools, and whether a defined tool is registered is a
// separate question this test does not answer.
func TestDocumentedToolRegistryMatchesTheCode(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file, so the comparison cannot be trusted")
	}
	root := filepath.Dir(filepath.Dir(thisFile)) // tool/ -> module root

	defined := toolNamesDefinedInCode(t, root)
	documented := toolNamesInRegistryTable(t, filepath.Join(root, "CODEBASE.md"))

	// Control each scan before trusting its verdict: a broken pattern or a moved
	// table would empty one side, and every missing row would then look like
	// agreement.
	if len(defined) < 20 {
		t.Fatalf("found only %d tool names in tool/ and lsp/tools.go; the scan is broken, not the docs", len(defined))
	}
	if len(documented) < 20 {
		t.Fatalf("found only %d rows in CODEBASE.md's registry table; the scan is broken, not the code", len(documented))
	}

	// mcp_* is a runtime family whose tools are named by the server, not a name
	// this package defines.
	delete(documented, "mcp_*")

	for _, name := range sortedNames(defined) {
		if !documented[name] {
			t.Errorf("CODEBASE.md's Complete Tool Name Registry does not list %q, which tool/ defines", name)
		}
	}
	for _, name := range sortedNames(documented) {
		if !defined[name] {
			t.Errorf("CODEBASE.md's Complete Tool Name Registry lists %q, which no code defines", name)
		}
	}
}

// toolNamesDefinedInCode reads the tool names the code defines: the `Name: "…"`
// literals in this package's non-test files, plus the `lsp` tool types that
// `main.go` registers through `lsp.ToolFactory`.
func toolNamesDefinedInCode(t *testing.T, root string) map[string]bool {
	t.Helper()
	out := map[string]bool{}

	dir := filepath.Join(root, "tool")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("cannot read %s: %v", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("cannot read %s: %v", name, err)
		}
		for _, m := range toolNameLiteral.FindAllStringSubmatch(string(src), -1) {
			out[m[1]] = true
		}
	}

	// The lsp package cannot import this one, so its four tool names live there.
	lsp := filepath.Join(root, "lsp", "tools.go")
	src, err := os.ReadFile(lsp)
	if err != nil {
		t.Fatalf("cannot read %s: %v", lsp, err)
	}
	for _, m := range lspToolType.FindAllStringSubmatch(string(src), -1) {
		out[m[1]] = true
	}
	return out
}

// toolNamesInRegistryTable returns the names in CODEBASE.md's "Complete Tool
// Name Registry" table. Only that table is read: the file holds other tables
// whose first column is a package, a plan or an agent name.
func toolNamesInRegistryTable(t *testing.T, path string) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}

	const heading = "### Complete Tool Name Registry"
	lines := strings.Split(string(src), "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s no longer has a %q heading; this test reads that table", path, heading)
	}

	out := map[string]bool{}
	for _, line := range lines[start+1:] {
		if strings.HasPrefix(line, "### ") {
			break
		}
		if m := registryRow.FindStringSubmatch(line); m != nil {
			out[m[1]] = true
		}
	}
	return out
}

func sortedNames(set map[string]bool) []string {
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
