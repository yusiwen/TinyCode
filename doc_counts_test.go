package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestTheDocumentedCountsMatchTheTree is the gate issue #241 asks for: the two
// numbers the repository states about itself — the scenario-suite size in
// AGENTS.md and the test-function total in README.md and CODEBASE.md — are
// recomputed from the tree and compared with the text.
//
// The counting rules are the documented commands' rules on purpose: "a file whose
// name ends in _test.go, outside the nested tuiprobe module, with a line that
// begins `func Test`" is what `find … -print0 | xargs -0 grep -h '^func Test' |
// wc -l` means, and a scenario file is `tui/testdata/scenarios/*.scenario`. A
// gate whose rule drifted from its command would be checking something other than
// what the document claims.
//
// Issue #170 removed the README's test-count badge rather than gate it, and #241
// found the same drift in two documents a month later. The difference here is that
// these two counts are cheap and exact, which is what makes a gate affordable;
// the coverage table beside them is a snapshot with a caption instead, because a
// percentage moves with every PR and two runs of the same command on the same tree
// already differ (see CODEBASE.md's testing section).
func TestTheDocumentedCountsMatchTheTree(t *testing.T) {
	scenarios, err := countScenarioFiles(".")
	if err != nil {
		t.Fatalf("count scenario files: %v", err)
	}
	tests, err := countTestFunctions(".")
	if err != nil {
		t.Fatalf("count test functions: %v", err)
	}
	if scenarios == 0 || tests == 0 {
		t.Fatalf("the counters found nothing (scenarios=%d tests=%d); the gate would pass vacuously", scenarios, tests)
	}

	assertDocumented(t, "AGENTS.md", `There are (\d+) files today`, scenarios, "scenario files")
	for _, doc := range []string{"README.md", "CODEBASE.md"} {
		assertDocumented(t, doc, `(\d+) test functions`, tests, "test functions")
	}
	t.Logf("the tree has %d scenario files and %d test functions", scenarios, tests)
}

// assertDocumented compares every number the pattern finds in path with want.
func assertDocumented(t *testing.T, path, pattern string, want int, what string) {
	t.Helper()
	found, err := documentedNumbers(path, pattern)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(found) == 0 {
		t.Fatalf("%s no longer states a %s count in the form this gate reads (%s)", path, what, pattern)
	}
	for _, got := range found {
		if got != want {
			t.Errorf("%s says %d %s, the tree has %d — run the command in the surrounding prose and update the number", path, got, what, want)
		}
	}
}

// documentedNumbers returns every capture group 1 of pattern in path.
func documentedNumbers(path, pattern string) ([]int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(pattern)
	var numbers []int
	for _, match := range re.FindAllStringSubmatch(string(data), -1) {
		n, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		numbers = append(numbers, n)
	}
	return numbers, nil
}

// countScenarioFiles counts the black-box scenario files.
func countScenarioFiles(root string) (int, error) {
	entries, err := os.ReadDir(filepath.Join(root, "tui", "testdata", "scenarios"))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".scenario") {
			n++
		}
	}
	return n, nil
}

// countTestFunctions counts the lines that define a test, in the main module.
func countTestFunctions(root string) (int, error) {
	n := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable directory is not this gate's business
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "tuiprobe", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "func Test") {
				n++
			}
		}
		return nil
	})
	return n, err
}
