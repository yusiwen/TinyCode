package tool

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// recordingCallSites is the pinned census of this package's calls that change a
// file's content, and therefore have to record a pre-image (issue #234).
//
// The census is what makes the property checkable instead of hoped for: a new
// write site fails this test until it is added here deliberately. That is the
// same defence the roadmap's reference sweep uses — a renamed file and an
// out-of-range line each make it fail — and it exists because the alternative
// (trusting every future author to remember) is how `apply_patch`'s delete
// operation came to bypass the write path in the first place.
//
// The key is the helper that records; the value maps `<file>:<function>` to how
// many times that function calls it.
var recordingCallSites = map[string]map[string]int{
	"writeSandboxed": {
		"edit.go:Edit":              1,
		"apply_patch.go:ApplyPatch": 2, // update, and add
		"filesystem.go:WriteFile":   1,
	},
	"removeSandboxed": {
		"apply_patch.go:ApplyPatch": 1, // delete
	},
}

func TestEveryContentChangeGoesThroughARecordingCall(t *testing.T) {
	found := map[string]map[string]int{}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				if _, pinned := recordingCallSites[ident.Name]; !pinned {
					return true
				}
				if len(call.Args) == 0 || isFreshContext(call.Args[0]) {
					t.Errorf("%s: %s is called with a context that carries no tool-call scope, so the write could not be undone",
						fset.Position(call.Pos()), ident.Name)
				}
				if found[ident.Name] == nil {
					found[ident.Name] = map[string]int{}
				}
				found[ident.Name][name+":"+fn.Name.Name]++
				return true
			})
		}
	}

	if !sameCensus(found, recordingCallSites) {
		t.Errorf("the content-changing call sites moved:\n  found:  %s\n  pinned: %s\n"+
			"if this is a new write path, give it a context that carries the tool-call scope and add it here;\n"+
			"if it is the checkpoint store writing its own files, it does not belong in this census — the store is not a tool path",
			formatCensus(found), formatCensus(recordingCallSites))
	}
}

// isFreshContext reports whether an argument is context.Background() or
// context.TODO(): both are the shape a new call site takes when the author
// copies an older one, and both mean the write records nothing.
func isFreshContext(arg ast.Expr) bool {
	call, ok := arg.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "context" {
		return false
	}
	return sel.Sel.Name == "Background" || sel.Sel.Name == "TODO"
}

func sameCensus(a, b map[string]map[string]int) bool {
	return formatCensus(a) == formatCensus(b)
}

func formatCensus(census map[string]map[string]int) string {
	helpers := make([]string, 0, len(census))
	for helper := range census {
		helpers = append(helpers, helper)
	}
	sort.Strings(helpers)
	parts := make([]string, 0, len(helpers))
	for _, helper := range helpers {
		sites := make([]string, 0, len(census[helper]))
		for site, count := range census[helper] {
			sites = append(sites, site+" x"+strconv.Itoa(count))
		}
		sort.Strings(sites)
		parts = append(parts, helper+"["+strings.Join(sites, ", ")+"]")
	}
	return strings.Join(parts, " ")
}
