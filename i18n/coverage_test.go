package i18n

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoMessageIsPassedToASinkAsARawLiteral is the coverage half of the catalogue
// gate. The catalogue gate proves that every translated message is translated;
// this one proves that the messages the interface shows are translated at all.
//
// It guards the shape a new message takes when somebody adds one in a hurry — a
// literal handed straight to a status bar, a system message or a dialog. A
// message that arrives through a variable cannot be checked here, which is why
// TestTheTranslatedTablesHaveChineseEntries in package tui enumerates the tables
// that do that.
//
// Mutation-checked when written: unwrapping any one call (putting the literal
// back) makes it name that file and line.
func TestNoMessageIsPassedToASinkAsARawLiteral(t *testing.T) {
	root := moduleRoot(t)
	sinks := []*regexp.Regexp{
		regexp.MustCompile(`\.ShowStatus\(\s*"`),
		regexp.MustCompile(`\.appendSystem\(\s*"`),
		regexp.MustCompile(`\.showDialog\(\s*"`),
		regexp.MustCompile(`\.showDialogWithCancel\(\s*"`),
	}

	var offences []string
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
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		// The gate is about the interface, so it reads the two surfaces that
		// draw one: the TUI and the command line.
		if !strings.HasPrefix(rel, "tui"+string(filepath.Separator)) && rel != "main.go" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("%s: %w", rel, readErr)
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, re := range sinks {
				if re.MatchString(line) {
					offences = append(offences, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan the interface sources: %v", err)
	}
	for _, offence := range offences {
		t.Errorf("message passed to a sink without i18n.T: %s", offence)
	}
}
