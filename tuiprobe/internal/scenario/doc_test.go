package scenario

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// stepCase matches one case line of the step() switch, with one or more verb literals:
// `case "golden", "diff":`.
var stepCase = regexp.MustCompile(`(?m)^\tcase ((?:"[a-z-]+"(?:, )?)+):$`)

// verbLiteral pulls the verb out of such a case clause.
var verbLiteral = regexp.MustCompile(`"([a-z-]+)"`)

// TestDocumentedSteps keeps the reference table honest against the runner.
//
// docs/scenario.md lagged this switch once already: `screenshot` and its `shot` alias were
// handled, used by 25 of the consumer's 31 scenario files, and absent from the table — and
// nothing failed, because a table is prose (issue #142). So the verbs are read out of step()
// here, the switch being the authority, and each one must be documented.
//
// It parses the source on purpose. The other direction — every documented verb really is
// accepted — is covered by the example scenario, which runs the vocabulary against a program.
func TestDocumentedSteps(t *testing.T) {
	src, err := os.ReadFile("scenario.go")
	if err != nil {
		t.Fatalf("read scenario.go: %v", err)
	}
	start := strings.Index(string(src), "func (r *runner) step(s Step) error {")
	if start < 0 {
		t.Fatal("step() not found in scenario.go: this check reads the switch, so it must be updated with it")
	}
	block := string(src)[start:]
	if end := strings.Index(block, "\n}\n"); end >= 0 {
		block = block[:end]
	}

	verbs := map[string]bool{}
	for _, m := range stepCase.FindAllStringSubmatch(block, -1) {
		for _, v := range verbLiteral.FindAllStringSubmatch(m[1], -1) {
			verbs[v[1]] = true
		}
	}
	// Passing by finding nothing is the failure mode of a source-reading check.
	if len(verbs) < 10 {
		t.Fatalf("found %d verbs in step(), want the whole vocabulary: did the switch change shape?", len(verbs))
	}

	doc, err := os.ReadFile("../../docs/scenario.md")
	if err != nil {
		t.Fatalf("read docs/scenario.md: %v", err)
	}

	names := make([]string, 0, len(verbs))
	for v := range verbs {
		names = append(names, v)
	}
	sort.Strings(names)

	for _, v := range names {
		// The verb must be a backticked token of its own: `wait` must not be satisfied by
		// finding `wait-exit`.
		row := regexp.MustCompile("`" + regexp.QuoteMeta(v) + "([\\s<`])")
		if !row.Match(doc) {
			t.Errorf("step() handles %q but docs/scenario.md has no `%s` row: an author reads the "+
				"table, so the runner and its reference must not drift (issue #142)", v, v)
		}
	}
}
