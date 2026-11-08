package main

// The regression baseline (roadmap F1's last piece): a stored measurement that a
// later run is compared against, so "this change made the agent worse" is a
// reading rather than an impression.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// regression is the comparison's outcome.
type regression struct {
	// Comparable is false when the two runs cannot be compared at all: a different
	// corpus, or a stored file without the attempts it needs.
	Comparable bool `json:"comparable"`
	// Rule is the decision rule, written down so a reader can disagree with it.
	Rule string `json:"rule"`
	// Regressed and Improved are "configuration/task" names.
	Regressed []string `json:"regressed,omitempty"`
	Improved  []string `json:"improved,omitempty"`
	// Unchanged counts the tasks the rule could decide, and Inconclusive the tasks
	// it declined to (a partial pass on either side).
	Unchanged    int `json:"unchanged"`
	Inconclusive int `json:"inconclusive"`
	// Note says why the comparison is not comparable, when it is not.
	Note string `json:"note,omitempty"`
}

// regressionRule is the pre-registered test. With a handful of repetitions the
// noise is not measured, so only an unambiguous flip counts: a task the old run
// solved in *every* attempt and the new run solved in *none* is a regression, and
// the mirror is an improvement. Anything else is inconclusive and said so.
const regressionRule = "a task is regressed when the baseline solved it in every attempt and this run solved it in none; improved is the mirror; every other case is inconclusive"

// compareBaselines compares a stored report with a fresh one.
func compareBaselines(baseline, current baselineReport) regression {
	reg := regression{Comparable: true, Rule: regressionRule}
	if baseline.Corpus != "" && current.Corpus != "" && baseline.Corpus != current.Corpus {
		reg.Comparable = false
		reg.Note = fmt.Sprintf("the corpus changed since the baseline (%s → %s); per-task comparison would be meaningless", shortDigest(baseline.Corpus), shortDigest(current.Corpus))
		return reg
	}
	if len(baseline.Configurations) == 0 || len(baseline.Results) == 0 {
		reg.Comparable = false
		reg.Note = "the baseline file has no configurations or no attempts to compare against"
		return reg
	}

	oldTasks := taskOutcomes(baseline)
	nowTasks := taskOutcomes(current)
	for key, old := range oldTasks {
		now, ok := nowTasks[key]
		if !ok {
			continue
		}
		switch {
		case old.passes == old.reps && now.passes == 0:
			reg.Regressed = append(reg.Regressed, key)
		case old.passes == 0 && now.passes == now.reps:
			reg.Improved = append(reg.Improved, key)
		case old.passes == old.reps && now.passes == now.reps:
			reg.Unchanged++
		case old.passes == 0 && now.passes == 0:
			reg.Unchanged++
		default:
			reg.Inconclusive++
		}
	}
	sort.Strings(reg.Regressed)
	sort.Strings(reg.Improved)
	return reg
}

// outcome is one task's attempts in one configuration.
type outcome struct {
	passes int
	reps   int
}

// taskOutcomes indexes a report's tasks by "configuration/task".
func taskOutcomes(report baselineReport) map[string]outcome {
	outcomes := make(map[string]outcome)
	for _, cfg := range report.Configurations {
		for _, tk := range cfg.Tasks {
			outcomes[cfg.Name+"/"+tk.Task] = outcome{passes: tk.Passes, reps: tk.Reps}
		}
	}
	return outcomes
}

// corpusDigest fingerprints a repository-task corpus: the task names and their
// TASK files, so a baseline says which corpus it measured.
func corpusDigest(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	hash := sha256.New()
	for _, name := range names {
		fmt.Fprintf(hash, "task %s\n", name)
		data, err := os.ReadFile(filepath.Join(dir, name, "TASK"))
		if err != nil {
			continue
		}
		hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// shortDigest is the first eight characters, for a message a person reads.
func shortDigest(digest string) string {
	if len(digest) > 8 {
		return digest[:8]
	}
	return digest
}

// printRegression writes the comparison, which is the section a release decision
// is read from.
func printRegression(w io.Writer, reg regression) {
	fmt.Fprintf(w, "\n## Regression against the baseline\n\n")
	if !reg.Comparable {
		fmt.Fprintf(w, "Not comparable — %s.\n", reg.Note)
		return
	}
	fmt.Fprintf(w, "Rule: %s.\n\n", reg.Rule)
	switch {
	case len(reg.Regressed) > 0:
		fmt.Fprintf(w, "**Regressed (%d):** %s\n\n", len(reg.Regressed), strings.Join(reg.Regressed, ", "))
	case len(reg.Improved) > 0:
		fmt.Fprintf(w, "No regressions; **improved (%d):** %s\n\n", len(reg.Improved), strings.Join(reg.Improved, ", "))
	default:
		fmt.Fprintf(w, "No regressions and no improvements the rule can decide.\n\n")
	}
	fmt.Fprintf(w, "Unchanged: %d task(s) decided; inconclusive: %d (a partial pass on one side).\n", reg.Unchanged, reg.Inconclusive)
}

// loadBaseline reads a stored baseline.json.
func loadBaseline(path string) (baselineReport, error) {
	var report baselineReport
	data, err := os.ReadFile(path)
	if err != nil {
		return report, err
	}
	if err := json.Unmarshal(data, &report); err != nil {
		return report, fmt.Errorf("parse %s: %w", path, err)
	}
	return report, nil
}
