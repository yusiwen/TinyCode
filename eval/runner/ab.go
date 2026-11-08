package main

// The A/B verdict (roadmap F2): what "compare two variants" means, decided before
// the data.

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// abComparison is one ordered pair of variants.
type abComparison struct {
	A string `json:"a"`
	B string `json:"b"`
	// OnlyA and OnlyB are the tasks exactly one side solved (any attempt).
	OnlyA []string `json:"only_a,omitempty"`
	OnlyB []string `json:"only_b,omitempty"`
	// Winner is "" when neither side dominates.
	Winner string `json:"winner,omitempty"`
	// Detail says why, in words a reader can disagree with.
	Detail string `json:"detail"`
}

// abRule is the pre-registered test. "Better" is dominance, not a mean: a variant
// that solves strictly more tasks and keeps every task the other solved is better;
// anything else — including one more solve bought by losing another — is not
// decidable from a handful of repetitions, and is reported as inconclusive with
// the per-task difference listed.
const abRule = "a variant is better when it solves strictly more tasks and solves every task the other solved; any other difference is inconclusive"

// compareVariants pairs every variant with every later one.
func compareVariants(report baselineReport) []abComparison {
	rules := make([]abComparison, 0)
	for i := 0; i < len(report.Configurations); i++ {
		for j := i + 1; j < len(report.Configurations); j++ {
			rules = append(rules, compareVariantPair(report.Configurations[i], report.Configurations[j]))
		}
	}
	return rules
}

// compareVariantPair compares two configurations task by task.
//
// "Solved" means solved by a majority of its repetitions, the same unit the rest
// of the report counts, so a variant cannot win by being lucky twice on a task the
// other failed twice.
func compareVariantPair(a, b configSummary) abComparison {
	comparison := abComparison{A: a.Name, B: b.Name}
	solvedA := solvedTasks(a)
	solvedB := solvedTasks(b)
	for _, name := range sortedKeys(solvedA) {
		if _, ok := solvedB[name]; !ok {
			comparison.OnlyA = append(comparison.OnlyA, name)
		}
	}
	for _, name := range sortedKeys(solvedB) {
		if _, ok := solvedA[name]; !ok {
			comparison.OnlyB = append(comparison.OnlyB, name)
		}
	}
	switch {
	case len(comparison.OnlyA) > 0 && len(comparison.OnlyB) == 0:
		comparison.Winner = a.Name
		comparison.Detail = fmt.Sprintf("%s solves %d task(s) %s does not, and keeps every task %s solves", a.Name, len(comparison.OnlyA), b.Name, b.Name)
	case len(comparison.OnlyB) > 0 && len(comparison.OnlyA) == 0:
		comparison.Winner = b.Name
		comparison.Detail = fmt.Sprintf("%s solves %d task(s) %s does not, and keeps every task %s solves", b.Name, len(comparison.OnlyB), a.Name, a.Name)
	case len(comparison.OnlyA) == 0 && len(comparison.OnlyB) == 0:
		comparison.Detail = "both variants solved the same tasks"
	default:
		comparison.Detail = fmt.Sprintf("each solves tasks the other does not (%s: %s; %s: %s), so neither dominates",
			a.Name, strings.Join(comparison.OnlyA, ", "), b.Name, strings.Join(comparison.OnlyB, ", "))
	}
	return comparison
}

// solvedTasks is the set of tasks a configuration solved by majority.
func solvedTasks(cfg configSummary) map[string]bool {
	solved := make(map[string]bool)
	for _, task := range cfg.Tasks {
		if task.Solved {
			solved[task.Task] = true
		}
	}
	return solved
}

// sortedKeys makes the report deterministic.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// printAB writes the comparison section.
func printAB(w io.Writer, comparisons []abComparison) {
	fmt.Fprintf(w, "\n## A/B comparison\n\n")
	fmt.Fprintf(w, "Rule: %s. Solve status is by majority of repetitions, the same unit the tables above count; a task solved by exactly one side is listed, so a winner on count alone cannot hide a lost task.\n\n", abRule)
	for _, comparison := range comparisons {
		line := fmt.Sprintf("- **%s vs %s** — %s", comparison.A, comparison.B, comparison.Detail)
		if comparison.Winner != "" {
			line = fmt.Sprintf("- **%s vs %s** — winner: **%s**. %s", comparison.A, comparison.B, comparison.Winner, comparison.Detail)
		}
		fmt.Fprintf(w, "%s\n", line)
	}
}
