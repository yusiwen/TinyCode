package tool

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// TestRefusalVocabularyHasOneDefinition is the source-level half of the
// contract: the marker and the hints live in one file, no call site formats a
// marker of its own, and the prefixes this replaced are gone rather than left
// beside the new one.
//
// It reads the tree rather than the behaviour on purpose: a call site that
// writes its own marker is invisible to a behavioural test that happens not to
// exercise that path, and the drift is exactly what this vocabulary exists to
// stop.
func TestRefusalVocabularyHasOneDefinition(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file, so the scan cannot be trusted")
	}
	root := filepath.Dir(filepath.Dir(thisFile)) // tool/ -> module root

	legacy := []string{"[SECURITY", "PLAN MODE BLOCKED"}
	markerLiteral := `"` + types.RefusalMarker + `"`
	vocabularyFile := "refusal.go"

	scanned := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".worktrees", "node_modules", "tuiprobe":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Test files are out of scope: asserting the marker is how a test
		// checks it, and this scan would otherwise trip on its own vocabulary.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(src)
		rel, _ := filepath.Rel(root, path)
		scanned++

		for _, prefix := range legacy {
			if strings.Contains(text, prefix) {
				t.Errorf("%s still carries the refusal prefix %q; the vocabulary is types.Refusal", rel, prefix)
			}
		}
		if strings.Contains(text, markerLiteral) && filepath.Base(path) != vocabularyFile {
			t.Errorf("%s formats its own refusal marker; build a types.Refusal instead", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scanning the tree: %v", err)
	}
	if scanned < 10 {
		t.Fatalf("the scan read only %d Go files, which cannot be the whole tree", scanned)
	}
}
