package scenario

import (
	"os"
	"regexp"
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
// nothing failed, because a table is prose (issue #142). The CLI help drifted the same way
// and listed ten of the thirteen verbs (issue #145).
//
// So `Verbs()` is the authority now, and this test holds two of the three surfaces to it in
// both directions: the switch that runs the verbs, and the table an author reads. The third —
// the CLI help — is generated from the same list and checked in package cli.
//
// It parses the sources on purpose. The other property — every listed verb really is
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

	authority := map[string]bool{}
	for _, v := range Verbs() {
		authority[v] = true
	}
	for v := range verbs {
		if !authority[v] {
			t.Errorf("step() accepts %q but Verbs() does not list it: the help and the table are "+
				"rendered from Verbs(), so the verb would be runnable and undocumented (issue #145)", v)
		}
	}
	for v := range authority {
		if !verbs[v] {
			t.Errorf("Verbs() lists %q but step() does not accept it: every surface would advertise "+
				"a step that fails at line one", v)
		}
	}

	doc, err := os.ReadFile("../../docs/scenario.md")
	if err != nil {
		t.Fatalf("read docs/scenario.md: %v", err)
	}
	documented := documentedVerbs(string(doc))
	if len(documented) < 10 {
		t.Fatalf("found %d documented verbs, want the whole table: did the table change shape?", len(documented))
	}
	for v := range authority {
		if !documented[v] {
			t.Errorf("Verbs() lists %q but docs/scenario.md has no `%s` row: an author reads the "+
				"table, so the runner and its reference must not drift (issue #142)", v, v)
		}
	}
	for v := range documented {
		if !authority[v] {
			t.Errorf("docs/scenario.md documents %q, which the runner does not accept", v)
		}
	}
}

// stepRow matches the first cell of a row in the reference's step table. The verb is
// backticked, and a row that documents flags (`golden --ansi <file>`) starts with the
// same verb, so one row per spelling is enough.
var stepRow = regexp.MustCompile("(?m)^\\| `([a-z-]+)")

// documentedVerbs reads the verbs out of the step table in docs/scenario.md, in the
// "## Steps" section only: the prose elsewhere names verbs too, and a mention is not a row.
func documentedVerbs(doc string) map[string]bool {
	section := doc
	if start := strings.Index(doc, "\n## Steps"); start >= 0 {
		section = doc[start+1:]
		if end := strings.Index(section, "\n## "); end >= 0 {
			section = section[:end]
		}
	}
	out := map[string]bool{}
	for _, m := range stepRow.FindAllStringSubmatch(section, -1) {
		out[m[1]] = true
	}
	return out
}

// backtickedStep matches a verb written as code in prose.
var backtickedStep = regexp.MustCompile("`([a-z-]+)`")

// TestConsumerDocsNameTheSteps keeps the two lists a *consumer* reads in step with the
// vocabulary: the workspace guide an agent loads, and the verification document.
//
// Both are prose, and both had drifted the same way as the CLI help: twelve verbs were
// named and `shot` was never one of them (issue #145), and `mark` joined the vocabulary
// later (issue #126). A reader there cannot know a step exists if the sentence that
// enumerates them does not name it.
func TestConsumerDocsNameTheSteps(t *testing.T) {
	authority := map[string]bool{}
	for _, v := range Verbs() {
		authority[v] = true
	}
	for _, path := range []string{"../../../AGENTS.md", "../../../docs/tui-verification.md"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(body)
		start := strings.Index(text, "teps are ")
		if start < 0 {
			t.Errorf("%s no longer says \"Steps are ...\"; this check reads that sentence", path)
			continue
		}
		list := text[start:]
		if end := strings.Index(list, ";"); end >= 0 {
			list = list[:end]
		}

		named := map[string]bool{}
		for _, m := range backtickedStep.FindAllStringSubmatch(list, -1) {
			named[m[1]] = true
		}
		if len(named) < 10 {
			t.Errorf("%s: found %d step names, want the whole list: did the sentence change shape?", path, len(named))
			continue
		}
		for v := range named {
			if !authority[v] {
				t.Errorf("%s names %q, which the runner does not accept", path, v)
			}
		}
		for v := range authority {
			if !named[v] {
				t.Errorf("%s does not name %q: a reader there cannot know the step exists (issue #145)", path, v)
			}
		}
	}
}
