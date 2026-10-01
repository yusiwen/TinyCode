package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRipgrep puts an executable `rg` on PATH and returns the file it writes its
// argv to, so a test can assert both what the fast path does with a tool and what
// it passes it. The body is a shell script; PATH is narrowed to its directory by
// the caller when the fall-through to the portable search is what is under test.
func fakeRipgrep(t *testing.T, body string) (binDir, argsFile string) {
	t.Helper()
	binDir = t.TempDir()
	argsFile = filepath.Join(t.TempDir(), "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$RG_ARGS\"\n" + body
	if err := os.WriteFile(filepath.Join(binDir, "rg"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake rg: %v", err)
	}
	t.Setenv("RG_ARGS", argsFile)
	return binDir, argsFile
}

// TestSearchWithRG covers the ripgrep fast path against a stand-in: the flags it
// passes, the output it returns, ripgrep's "exit 1 means no matches" convention,
// and a genuine failure.
func TestSearchWithRG(t *testing.T) {
	binDir, argsFile := fakeRipgrep(t, "printf 'main.go\\n3:func foo() {}\\n'\n")
	t.Setenv("PATH", binDir)

	out, err := searchWithRG(context.Background(), "foo", "/tmp/tree", "*.go")
	if err != nil {
		t.Fatalf("searchWithRG: %v", err)
	}
	if want := "main.go\n3:func foo() {}"; out != want {
		t.Errorf("result = %q, want %q", out, want)
	}

	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read the fake's argv: %v", err)
	}
	for _, want := range []string{"--line-number", "--heading", "--color\nnever", "--glob\n*.go", "foo", "/tmp/tree"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("argv is missing %q:\n%s", want, args)
		}
	}

	// No glob: the flag must not appear.
	binDir, argsFile = fakeRipgrep(t, "printf 'x.go\\n1:foo\\n'\n")
	t.Setenv("PATH", binDir)
	if _, err := searchWithRG(context.Background(), "foo", "/tmp/tree", ""); err != nil {
		t.Fatalf("searchWithRG without a glob: %v", err)
	}
	if args, _ := os.ReadFile(argsFile); strings.Contains(string(args), "--glob") {
		t.Errorf("--glob was passed although no glob was requested:\n%s", args)
	}
}

// TestSearchWithRGNoMatchesAndFailure pins ripgrep's exit-code contract: 1 means
// "no matches" and is not an error, while anything else is a failure that carries
// the tool's stderr.
func TestSearchWithRGNoMatchesAndFailure(t *testing.T) {
	binDir, _ := fakeRipgrep(t, "exit 1\n")
	t.Setenv("PATH", binDir)
	out, err := searchWithRG(context.Background(), "foo", "/tmp/tree", "")
	if err != nil {
		t.Fatalf("exit 1 must not be an error: %v", err)
	}
	if out != "No matches found." {
		t.Errorf("result = %q, want the no-match message", out)
	}

	binDir, _ = fakeRipgrep(t, "echo 'rg: unrecognized flag' >&2\nexit 2\n")
	t.Setenv("PATH", binDir)
	_, err = searchWithRG(context.Background(), "foo", "/tmp/tree", "")
	if err == nil {
		t.Fatal("exit 2 must be an error")
	}
	if !strings.Contains(err.Error(), "rg failed") || !strings.Contains(err.Error(), "unrecognized flag") {
		t.Errorf("error = %v, want it to name the failure and carry stderr", err)
	}
}

// TestSearchGoNative covers the portable search: matches with line numbers and a
// summary, no matches, the glob filter, and the two things the walk must skip
// (binary files and dotfiles or dot-directories).
func TestSearchGoNative(t *testing.T) {
	tree := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(tree, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package main\nfunc needle() {}\nvar x = needle\n")
	write("b.txt", "needle here\n")
	write("sub/c.go", "needle\n")
	write("bin.dat", "\x00needle in a binary\n")
	write(".hidden.go", "needle\n")
	write(".git/config", "needle\n")

	out, err := searchGoNative(context.Background(), "needle", tree, "")
	if err != nil {
		t.Fatalf("searchGoNative: %v", err)
	}
	if !strings.Contains(out, "Found 4 matches in 3 files") {
		t.Errorf("summary = %q, want 4 matches in 3 files (binary, dotfile and dot-directory skipped)", firstLine(out))
	}
	for _, want := range []string{"2:func needle() {}", "3:var x = needle", "b.txt", "c.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("result is missing %q:\n%s", want, out)
		}
	}
	for _, banned := range []string{"bin.dat", ".hidden.go", ".git"} {
		if strings.Contains(out, banned) {
			t.Errorf("result reports %q, which the walk must skip:\n%s", banned, out)
		}
	}

	// The glob filter keeps the walk to matching file names.
	globbed, err := searchGoNative(context.Background(), "needle", tree, "*.txt")
	if err != nil {
		t.Fatalf("searchGoNative with a glob: %v", err)
	}
	if !strings.Contains(globbed, "Found 1 matches in 1 files") || !strings.Contains(globbed, "b.txt") {
		t.Errorf("globbed result = %q, want only b.txt", firstLine(globbed))
	}

	// Nothing matches.
	out, err = searchGoNative(context.Background(), "absent", tree, "")
	if err != nil {
		t.Fatalf("searchGoNative without matches: %v", err)
	}
	if out != "No matches found." {
		t.Errorf("result = %q, want the no-match message", out)
	}

	// A pattern the regexp engine rejects is reported, not silently empty.
	if _, err := searchGoNative(context.Background(), "[", tree, ""); err == nil {
		t.Error("an invalid regex must be an error")
	}
	if _, err := searchGoNative(context.Background(), "needle", tree, "["); err == nil {
		t.Error("an invalid glob must be an error")
	}
}

// TestSearchGoNativeSkipsUnreadableDirectories covers the walk's error branch: a
// directory the process cannot read costs its files and nothing else.
func TestSearchGoNativeSkipsUnreadableDirectories(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode 000 does not deny access")
	}

	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "readable.go"), []byte("needle\n"), 0644); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(tree, "locked")
	if err := os.MkdirAll(locked, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "hidden.go"), []byte("needle\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0755) })

	out, err := searchGoNative(context.Background(), "needle", tree, "")
	if err != nil {
		t.Fatalf("an unreadable directory must not fail the search: %v", err)
	}
	if !strings.Contains(out, "Found 1 matches in 1 files") {
		t.Errorf("result = %q, want only the readable file", firstLine(out))
	}
}

// TestSearchLadderFallsThroughToThePortableSearch is the ladder gate of issue #4:
// a tool that is installed but broken must not fail the search while the portable
// implementation is right there. PATH holds only the broken `rg`, so the next rung
// is the Go walk — the shape a machine with a broken ripgrep shim has.
func TestSearchLadderFallsThroughToThePortableSearch(t *testing.T) {
	broken, _ := fakeRipgrep(t, "echo 'rg: broken shim' >&2\nexit 2\n")
	t.Setenv("PATH", broken)

	previousRoot := DefaultSandbox.ProjectRoot
	DefaultSandbox.ProjectRoot = ""
	t.Cleanup(func() { DefaultSandbox.ProjectRoot = previousRoot })

	tree := t.TempDir()
	if err := os.WriteFile(filepath.Join(tree, "a.go"), []byte("package main\nfunc needle() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := SearchFiles().Execute(context.Background(), map[string]any{
		"pattern": "needle",
		"path":    tree,
	})
	if err != nil {
		t.Fatalf("search failed although the portable path could answer: %v", err)
	}
	if !strings.Contains(result, "Found 1 matches in 1 files") {
		t.Errorf("result = %q, want the portable search's summary", firstLine(result))
	}
}

// firstLine keeps a failure message readable when the result is a whole file.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
