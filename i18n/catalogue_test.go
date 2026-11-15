package i18n

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestTheCatalogueMatchesEveryCall is the gate the catalogue is built on: the
// source text is the message id, so a translated message and a call site are the
// same string, and the two are checked against each other rather than by hand.
//
// Three failures are possible and each was mutation-checked when the gate was
// written: a msgid with no Chinese entry (delete one entry), an entry whose verb
// count disagrees with its msgid (change a %d to %s), and an entry no source file
// mentions (add an entry for text that does not exist). The third check reads the
// raw literals of every file as well as the T calls, because a message reached
// through a table — the command palette, the key hints, the permission answers —
// is translated by a dynamic call and never appears as i18n.T("…") in the source.
func TestTheCatalogueMatchesEveryCall(t *testing.T) {
	root := moduleRoot(t)
	sources, err := scanSources(root)
	if err != nil {
		t.Fatalf("scan the module: %v", err)
	}
	if len(sources.calls) == 0 {
		t.Fatal("the scan found no i18n.T calls; the gate would pass vacuously")
	}

	var missing []string
	for msgid := range sources.calls {
		if _, ok := Translation(Chinese, msgid); !ok {
			missing = append(missing, msgid)
		}
	}
	sort.Strings(missing)
	for _, msgid := range missing {
		t.Errorf("no Chinese entry for %q", msgid)
	}

	var unused []string
	for msgid := range chinese {
		if _, called := sources.calls[msgid]; called {
			continue
		}
		if _, mentioned := sources.literals[msgid]; mentioned {
			continue
		}
		unused = append(unused, msgid)
	}
	sort.Strings(unused)
	for _, msgid := range unused {
		t.Errorf("catalogue entry %q is not mentioned by any source file", msgid)
	}

	var wrongVerbs []string
	for msgid, translation := range chinese {
		if got, want := verbSpec(translation), verbSpec(msgid); got != want {
			wrongVerbs = append(wrongVerbs, fmt.Sprintf("%q: verbs %q in the translation, %q in the msgid", msgid, got, want))
		}
	}
	sort.Strings(wrongVerbs)
	for _, line := range wrongVerbs {
		t.Error(line)
	}

	t.Logf("%d translated messages, %d i18n.T call sites", len(chinese), len(sources.calls))
}

// TestThePermissionPrefixesAgree guards the one place a translation is compared,
// not only shown: the permission dialog decides "always allow" with
// strings.HasPrefix against the prefix, so the translated full label has to begin
// with the translated prefix. Otherwise the most permissive answer silently
// becomes "deny".
func TestThePermissionPrefixesAgree(t *testing.T) {
	prefix, ok := Translation(Chinese, "Always allow")
	if !ok {
		t.Fatal("no Chinese entry for the always-allow prefix")
	}
	for _, msgid := range []string{"Always allow (saves to %s)", "Always allow (saves to your config)"} {
		full, ok := Translation(Chinese, msgid)
		if !ok {
			t.Errorf("no Chinese entry for %q", msgid)
			continue
		}
		if !strings.HasPrefix(full, prefix) {
			t.Errorf("%q must start with the translated prefix %q, got %q", msgid, prefix, full)
		}
	}
}

// verbSpec returns the fmt verbs of a message in order, so two translations of
// one message can be compared for both how many verbs they have and what kind:
// a %d that became %s passes a count but renders "%!s(int=3)" on screen. Escaped
// percent signs are not verbs and are skipped.
func verbSpec(s string) string {
	var verbs []string
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '%' {
			i++
			continue
		}
		j := i + 1
		for j < len(s) && strings.ContainsRune("-+ 0-9.#[]", rune(s[j])) {
			j++
		}
		if j < len(s) {
			verbs = append(verbs, s[i:j+1])
			i = j
		}
	}
	return strings.Join(verbs, " ")
}

// sourceScan is what one pass over the module collects: the msgids that appear in
// a T call, and every string literal in the scanned files.
type sourceScan struct {
	calls    map[string]bool
	literals map[string]bool
}

// scanSources parses every non-test Go file in the main module. The nested
// tuiprobe module is skipped: it does not import this package, and walking it
// would make this gate depend on a tool's source for no reason.
func scanSources(root string) (sourceScan, error) {
	out := sourceScan{calls: map[string]bool{}, literals: map[string]bool{}}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if d != nil && d.IsDir() {
				switch d.Name() {
				case ".git", "tuiprobe", "bin", ".worktrees", "node_modules", "testdata":
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.BasicLit:
				if v.Kind == token.STRING {
					if text, err := strconv.Unquote(v.Value); err == nil {
						out.literals[text] = true
					}
				}
			case *ast.CallExpr:
				sel, ok := v.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "T" || len(v.Args) == 0 {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "i18n" {
					return true
				}
				if lit, ok := v.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if text, err := strconv.Unquote(lit.Value); err == nil {
						out.calls[text] = true
					}
				}
			}
			return true
		})
		return nil
	})
	return out, err
}

// moduleRoot returns the directory that holds go.mod, found from this file.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = parent
	}
}
