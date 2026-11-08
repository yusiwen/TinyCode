package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reportOf builds a minimal report: one task per (config, task, passes, reps).
func reportOf(corpus string, tasks ...struct {
	config string
	task   string
	passes int
	reps   int
}) baselineReport {
	byConfig := map[string][]taskSummary{}
	for _, tk := range tasks {
		byConfig[tk.config] = append(byConfig[tk.config], taskSummary{
			Task: tk.task, Passes: tk.passes, Reps: tk.reps,
			Solved: tk.passes*2 > tk.reps,
		})
	}
	report := baselineReport{Corpus: corpus, Reps: 2}
	for _, cfg := range []string{"build"} {
		if list, ok := byConfig[cfg]; ok {
			report.Configurations = append(report.Configurations, configSummary{Name: cfg, Tasks: list})
		}
	}
	report.Results = []runResult{{Configuration: "build", Task: "a"}}
	return report
}

type outcomeCase = struct {
	config string
	task   string
	passes int
	reps   int
}

// TestCompareBaselinesOnlyDecidesTheUnambiguousFlips is the pre-registered rule:
// with a handful of repetitions only an exact flip counts either way.
func TestCompareBaselinesOnlyDecidesTheUnambiguousFlips(t *testing.T) {
	baseline := reportOf("d1",
		outcomeCase{"build", "regressed", 2, 2},
		outcomeCase{"build", "improved", 0, 2},
		outcomeCase{"build", "still-solved", 2, 2},
		outcomeCase{"build", "still-broken", 0, 2},
		outcomeCase{"build", "partial", 1, 2},
	)
	current := reportOf("d1",
		outcomeCase{"build", "regressed", 0, 2},
		outcomeCase{"build", "improved", 2, 2},
		outcomeCase{"build", "still-solved", 2, 2},
		outcomeCase{"build", "still-broken", 0, 2},
		outcomeCase{"build", "partial", 0, 2},
	)

	reg := compareBaselines(baseline, current)
	if !reg.Comparable {
		t.Fatalf("not comparable: %s", reg.Note)
	}
	if len(reg.Regressed) != 1 || reg.Regressed[0] != "build/regressed" {
		t.Fatalf("regressed = %v", reg.Regressed)
	}
	if len(reg.Improved) != 1 || reg.Improved[0] != "build/improved" {
		t.Fatalf("improved = %v", reg.Improved)
	}
	// still-solved, still-broken: decided and unchanged. partial: 1/2 → 0/2 is a
	// change the rule declines to call.
	if reg.Unchanged != 2 || reg.Inconclusive != 1 {
		t.Fatalf("unchanged = %d, inconclusive = %d", reg.Unchanged, reg.Inconclusive)
	}
}

// TestCompareBaselinesRefusesADifferentCorpus: a per-task comparison against a
// corpus that changed is meaningless, and the report says so instead of guessing.
func TestCompareBaselinesRefusesADifferentCorpus(t *testing.T) {
	baseline := reportOf("aaaa", outcomeCase{"build", "a", 2, 2})
	current := reportOf("bbbb", outcomeCase{"build", "a", 0, 2})
	reg := compareBaselines(baseline, current)
	if reg.Comparable {
		t.Fatal("compared two different corpora")
	}
	if !strings.Contains(reg.Note, "corpus changed") {
		t.Fatalf("note = %q", reg.Note)
	}
	if len(reg.Regressed) != 0 {
		t.Fatalf("regressed = %v", reg.Regressed)
	}
}

// TestCompareBaselinesRefusesAFileWithNoAttempts: an empty stored report is not a
// zero baseline, it is a missing one.
func TestCompareBaselinesRefusesAFileWithNoAttempts(t *testing.T) {
	reg := compareBaselines(baselineReport{}, reportOf("d1", outcomeCase{"build", "a", 2, 2}))
	if reg.Comparable {
		t.Fatal("compared against an empty baseline")
	}
}

// TestCorpusDigestTracksTheTaskSpecs: the digest is what makes "same corpus" a
// fact rather than an assumption.
func TestCorpusDigestTracksTheTaskSpecs(t *testing.T) {
	dir := t.TempDir()
	write := func(task, body string) {
		path := filepath.Join(dir, task)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "TASK"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a", "base: 1\nfix: 2\n")
	write("b", "base: 3\nfix: 4\n")
	first, err := corpusDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := corpusDigest(dir)
	if first != again {
		t.Fatalf("not stable: %s vs %s", first, again)
	}

	// A changed commit is a changed corpus.
	write("b", "base: 3\nfix: 5\n")
	second, _ := corpusDigest(dir)
	if first == second {
		t.Fatal("the digest ignored a changed TASK")
	}

	// The order tasks are listed in does not matter.
	third := t.TempDir()
	for _, name := range []string{"b", "a"} {
		data, _ := os.ReadFile(filepath.Join(dir, name, "TASK"))
		path := filepath.Join(third, name)
		os.MkdirAll(path, 0o755)
		os.WriteFile(filepath.Join(path, "TASK"), data, 0o644)
	}
	fourth, _ := corpusDigest(third)
	if fourth != second {
		t.Fatalf("the digest depends on directory order: %s vs %s", fourth, second)
	}
}

// TestRegressionSectionIsRendered: the section is what a release decision reads.
func TestRegressionSectionIsRendered(t *testing.T) {
	report := reportOf("d1", outcomeCase{"build", "a", 0, 2})
	reg := compareBaselines(reportOf("d1", outcomeCase{"build", "a", 2, 2}), report)
	report.Regression = &reg
	var out strings.Builder
	printMarkdown(&out, report)
	if !strings.Contains(out.String(), "## Regression against the baseline") {
		t.Fatalf("the section is missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Regressed (1):** build/a") {
		t.Fatalf("the regression is not named:\n%s", out.String())
	}
}
