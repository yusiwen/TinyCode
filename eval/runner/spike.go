package main

// The repository-index spike (roadmap B4, issue #308).
//
// It needs three things the ordinary harness does not: tasks whose tests are
// hidden (so the agent cannot read where the defect is), a way to install those
// tests after the run, and a treatment arm that puts a symbol map in the
// prompt. They live here rather than in main.go because they exist for one
// question and the harness proper should not grow a second task format.

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// mapBudgetTokens bounds the injected digest. 2000 tokens is roughly a page and
// a half: enough for a small module's whole surface, small enough that the
// treatment stays comparable to the baseline's prompt.
const mapBudgetTokens = 2000

// loadSpikeTasks reads the spike's task directories. It is loadTasks with one
// difference: the tests are not in the task, they are under hiddenDir, and they
// are the digests the tamper check compares against once installed.
func loadSpikeTasks(dir, hiddenDir string) ([]task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read spike tasks: %w", err)
	}
	var tasks []task
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		prompt, err := os.ReadFile(filepath.Join(path, "PROMPT"))
		if err != nil {
			return nil, fmt.Errorf("task %s has no PROMPT: %w", entry.Name(), err)
		}
		if _, err := os.Stat(filepath.Join(path, "go.mod")); err != nil {
			return nil, fmt.Errorf("task %s has no go.mod: %w", entry.Name(), err)
		}
		tier, err := readTier(path)
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", entry.Name(), err)
		}
		hiddenTask := filepath.Join(hiddenDir, entry.Name())
		digests, err := testDigests(hiddenTask)
		if err != nil {
			return nil, err
		}
		if len(digests) == 0 {
			return nil, fmt.Errorf("task %s has no hidden test in %s", entry.Name(), hiddenTask)
		}
		tasks = append(tasks, task{
			Name:   entry.Name(),
			Dir:    path,
			Tier:   tier,
			Prompt: strings.TrimSpace(string(prompt)),
			Tests:  digests,
		})
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no spike tasks in %s", dir)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Name < tasks[j].Name })
	return tasks, nil
}

// installHiddenTests copies the hidden suite into the work copy after the run.
// Every file is overwritten, so a file the agent happened to create under the
// same name cannot shadow the assertions it is graded by.
func installHiddenTests(hiddenDir, taskName, workDir string) error {
	dir := filepath.Join(hiddenDir, taskName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("hidden tests for %s: %w", taskName, err)
	}
	installed := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(workDir, entry.Name()), data, 0o644); err != nil {
			return err
		}
		installed++
	}
	if installed == 0 {
		return fmt.Errorf("hidden suite for %s is empty: grading would pass vacuously", taskName)
	}
	return nil
}

// promptFor returns the prompt one attempt sends. The map arm prepends a symbol
// digest of the work copy and nothing else: the model gets no new capability,
// only the survey a reader would do first.
func promptFor(cfg configuration, tk task, workDir string) string {
	if !cfg.RepoMap {
		return tk.Prompt
	}
	digest, err := repoMapDigest(workDir, mapBudgetTokens)
	if err != nil || strings.TrimSpace(digest) == "" {
		// A map that cannot be built is the baseline, and the run says so in
		// its log rather than silently measuring a shorter prompt as treatment.
		return tk.Prompt + "\n\n(The symbol map could not be generated for this directory.)"
	}
	return "A symbol map of this directory follows. It lists every file, the symbols it exports and " +
		"the imports it uses; it is a survey, not a substitute for reading the code.\n\n" +
		digest + "\n\n" + tk.Prompt
}

// repoMapDigest renders a bounded, sorted digest of a Go tree.
//
// It is deliberately shallow: package, file, exported declarations with their
// signatures, and imports. That is the information a reader builds by skimming
// a tree, which is exactly the work the spike asks whether a map can replace.
func repoMapDigest(dir string, maxTokens int) (string, error) {
	type fileDigest struct {
		path    string
		pkg     string
		imports []string
		decls   []string
	}
	var files []fileDigest
	fset := token.NewFileSet()

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // an unreadable corner is not the map's business
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			rel = path
		}
		digest := fileDigest{path: rel, pkg: file.Name.Name}
		for _, imp := range file.Imports {
			digest.imports = append(digest.imports, strings.Trim(imp.Path.Value, `"`))
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() || d.Type == nil {
					continue
				}
				signature := renderedType(fset, d.Type)
				if signature == "" {
					continue
				}
				if d.Recv != nil && len(d.Recv.List) > 0 {
					digest.decls = append(digest.decls, fmt.Sprintf("func %s %s%s", renderedReceiver(fset, d.Recv.List[0]), d.Name.Name, signature))
				} else {
					digest.decls = append(digest.decls, "func "+d.Name.Name+signature)
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !ts.Name.IsExported() || ts.Type == nil {
						continue
					}
					kind := "type"
					switch ts.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					digest.decls = append(digest.decls, fmt.Sprintf("type %s %s", ts.Name.Name, kind))
				}
			}
		}
		files = append(files, digest)
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })

	var b strings.Builder
	for _, file := range files {
		fmt.Fprintf(&b, "%s (package %s)\n", file.path, file.pkg)
		if len(file.imports) > 0 {
			fmt.Fprintf(&b, "  imports: %s\n", strings.Join(file.imports, ", "))
		}
		for _, decl := range file.decls {
			fmt.Fprintf(&b, "  %s\n", decl)
		}
		// Budget in the estimator the agent uses (4 bytes per token), checked
		// after each file so the map never ends mid-signature.
		if len(b.String())/4 >= maxTokens {
			b.WriteString("  … (map truncated at its budget)\n")
			break
		}
	}
	return b.String(), nil
}

// renderedReceiver prints a method's receiver as "(s *Store)", name included:
// the name is part of the signature a reader would skim.
func renderedReceiver(fset *token.FileSet, field *ast.Field) string {
	typ := renderedType(fset, field.Type)
	if typ == "" {
		return "(?)"
	}
	if len(field.Names) == 0 {
		return "(" + typ + ")"
	}
	return "(" + field.Names[0].Name + " " + typ + ")"
}

// renderedType prints one type expression, without the leading "func" keyword
// the printer includes for a FuncType.
func renderedType(fset *token.FileSet, node ast.Expr) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return ""
	}
	return strings.TrimPrefix(buf.String(), "func")
}
