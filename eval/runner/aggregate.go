package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// The aggregation, kept apart from the orchestration so it can be tested without
// spending money: everything here is a pure function of the results.

// taskSummary is one (configuration, task) cell.
type taskSummary struct {
	Task         string         `json:"task"`
	Passes       int            `json:"passes"`
	Reps         int            `json:"reps"`
	Solved       bool           `json:"solved"`   // a majority of reps passed
	Agreed       bool           `json:"agreed"`   // every rep agreed
	Tampered     int            `json:"tampered"` // reps whose test files were edited
	MedianSteps  int            `json:"median_steps"`
	MedianTokens int            `json:"median_tokens"`
	Stops        map[string]int `json:"stops,omitempty"`
	Tools        map[string]int `json:"tools,omitempty"`
}

// configSummary is one configuration's row.
type configSummary struct {
	Name            string           `json:"name"`
	Mode            string           `json:"mode"`
	MaxTokensPerRun int              `json:"max_tokens_per_run,omitempty"`
	Tasks           []taskSummary    `json:"tasks"`
	Solved          int              `json:"solved"`
	TaskCount       int              `json:"task_count"`
	Passes          int              `json:"passes"`
	Runs            int              `json:"runs"`
	Agreement       float64          `json:"agreement"`
	MedianSteps     int              `json:"median_steps"`
	MedianTokens    int              `json:"median_tokens"`
	TokensPerSolved int              `json:"tokens_per_solved"`
	Cost            types.CostTotals `json:"cost,omitempty"`
	Stops           map[string]int   `json:"stops,omitempty"`
	Tools           map[string]int   `json:"tools,omitempty"`
	MissingRecords  int              `json:"missing_records"`
}

// verdict is the question the spike exists to answer, answered.
type verdict struct {
	Complete bool     `json:"complete"`
	Met      bool     `json:"met"`
	Order    []string `json:"order"`
	Detail   string   `json:"detail"`
}

// baselineReport is what gets committed.
type baselineReport struct {
	GeneratedAt    string          `json:"generated_at"`
	Reps           int             `json:"reps"`
	Runs           int             `json:"runs"`
	Models         map[string]int  `json:"models,omitempty"`
	Configurations []configSummary `json:"configurations"`
	Verdict        verdict         `json:"verdict"`
	Results        []runResult     `json:"results"`
}

// summarise folds the attempts into the report the spike is judged on.
func summarise(results []runResult, configs []configuration) (baselineReport, error) {
	if len(results) == 0 {
		return baselineReport{}, fmt.Errorf("no results")
	}
	report := baselineReport{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Runs:        len(results),
		Results:     results,
	}
	seenReps := make(map[int]bool)
	for _, result := range results {
		seenReps[result.Rep] = true
	}
	report.Reps = len(seenReps)

	for _, cfg := range configs {
		summary := configSummary{
			Name:            cfg.Name,
			Mode:            cfg.Mode,
			MaxTokensPerRun: cfg.MaxTokensPerRun,
			Stops:           make(map[string]int),
			Tools:           make(map[string]int),
		}
		byTask := make(map[string][]runResult)
		for _, result := range results {
			if result.Configuration != cfg.Name {
				continue
			}
			byTask[result.Task] = append(byTask[result.Task], result)
			if result.TrajectoryRun == "" {
				summary.MissingRecords++
			}
			if result.Stop != "" {
				summary.Stops[result.Stop]++
			}
			for tool, count := range result.Tools {
				summary.Tools[tool] += count
			}
			if result.Pass {
				summary.Passes++
			}
			summary.Runs++
			if len(result.Cost) > 0 {
				if summary.Cost == nil {
					summary.Cost = make(types.CostTotals)
				}
				for unit, amount := range result.Cost {
					summary.Cost[unit] += amount
				}
			}
		}

		taskNames := make([]string, 0, len(byTask))
		for name := range byTask {
			taskNames = append(taskNames, name)
		}
		sort.Strings(taskNames)
		var steps, tokens []int
		agreed := 0
		for _, name := range taskNames {
			cell := summariseTask(name, byTask[name])
			summary.Tasks = append(summary.Tasks, cell)
			if cell.Solved {
				summary.Solved++
			}
			if cell.Agreed {
				agreed++
			}
			for _, result := range byTask[name] {
				steps = append(steps, result.Steps)
				tokens = append(tokens, result.Tokens)
			}
		}
		summary.TaskCount = len(taskNames)
		summary.MedianSteps = medianInt(steps)
		summary.MedianTokens = medianInt(tokens)
		if summary.Solved > 0 {
			total := 0
			for _, token := range tokens {
				total += token
			}
			summary.TokensPerSolved = total / summary.Solved
		}
		if len(taskNames) > 0 {
			summary.Agreement = float64(agreed) / float64(len(taskNames))
		}
		report.Configurations = append(report.Configurations, summary)
	}

	report.Models = make(map[string]int)
	for _, result := range results {
		if result.Model != "" {
			report.Models[result.Model]++
		}
	}
	report.Verdict = judge(report.Configurations)
	return report, nil
}

// summariseTask folds one cell's attempts.
func summariseTask(name string, results []runResult) taskSummary {
	cell := taskSummary{Task: name, Reps: len(results), Stops: make(map[string]int), Tools: make(map[string]int)}
	var steps, tokens []int
	for _, result := range results {
		if result.Pass {
			cell.Passes++
		}
		if result.TestsModified {
			cell.Tampered++
		}
		if result.Stop != "" {
			cell.Stops[result.Stop]++
		}
		for tool, count := range result.Tools {
			cell.Tools[tool] += count
		}
		steps = append(steps, result.Steps)
		tokens = append(tokens, result.Tokens)
	}
	cell.Solved = cell.Passes*2 > cell.Reps // a majority
	cell.Agreed = cell.Passes == 0 || cell.Passes == cell.Reps
	cell.MedianSteps = medianInt(steps)
	cell.MedianTokens = medianInt(tokens)
	return cell
}

// judge answers the abandonment condition, stated before the data.
//
// It compares *solved tasks* rather than raw passes, because a task is the unit
// the measurement is about: three flaky reps of one task are not three tasks.
//
// The condition has three clauses, and the middle one was corrected before any
// data existed (see the issue's dated comment). The first version demanded
// "limited strictly more than plan", which is unsatisfiable when both solve
// nothing — and plan is read-only, so solving nothing is its expected result. A
// rule that fails for an arithmetic reason rather than a finding would have
// declared "abandon" before the run. The clauses now are:
//
//  1. build solves strictly more tasks than limited — the separation that matters;
//  2. limited solves at least as many as plan — a starved agent must not beat a
//     read-only one, and equality at zero is the expected floor, not a failure;
//  3. build solves at least half the task set — otherwise the tasks are beyond
//     the agent and what was measured is a floor, not a signal.
func judge(configs []configSummary) verdict {
	order := make([]string, 0, len(configs))
	byName := make(map[string]configSummary, len(configs))
	for _, cfg := range configs {
		order = append(order, cfg.Name)
		byName[cfg.Name] = cfg
	}
	sort.SliceStable(order, func(i, j int) bool {
		return byName[order[i]].Solved > byName[order[j]].Solved
	})

	result := verdict{Order: order}
	build, hasBuild := byName["build"]
	limited, hasLimited := byName["limited"]
	plan, hasPlan := byName["plan"]
	if !hasBuild || !hasLimited || !hasPlan {
		result.Detail = "the comparison needs all three configurations: build, limited and plan"
		return result
	}
	result.Complete = true
	summary := fmt.Sprintf("build %d/%d, limited %d/%d, plan %d/%d",
		build.Solved, build.TaskCount, limited.Solved, limited.TaskCount, plan.Solved, plan.TaskCount)
	switch {
	case build.Solved*2 < build.TaskCount:
		result.Detail = "the task set is beyond the agent, so the measurement is a floor — " + summary
	case build.Solved <= limited.Solved:
		result.Detail = "a budget-starved agent solved as many tasks as an unrestricted one, so the budget is not the variable — " + summary
	case limited.Solved < plan.Solved:
		result.Detail = "the read-only agent beat the starved one, which inverts the expected order — " + summary
	default:
		result.Met = true
		result.Detail = summary
	}
	return result
}

// medianInt returns the upper median: for an even count, the larger of the two
// middle values. A median is reported, not asserted, so rounding up is fine and
// a float would only invite a false precision.
func medianInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	return sorted[len(sorted)/2]
}

// writeBaseline writes the two committed artifacts.
func writeBaseline(dir string, report baselineReport) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "baseline.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	var markdown strings.Builder
	printMarkdown(&markdown, report)
	return os.WriteFile(filepath.Join(dir, "BASELINE.md"), []byte(markdown.String()), 0o644)
}

// printMarkdown renders the report for a human, which is how it gets read.
func printMarkdown(w io.Writer, report baselineReport) {
	fmt.Fprintf(w, "# The evaluation signal, measured\n\n")
	fmt.Fprintf(w, "Generated %s. `TINYCODE_EVAL=1 go run ./eval/runner`\n\n", report.GeneratedAt)
	fmt.Fprintf(w, "%d runs, %d repetitions per (configuration x task).\n\n", report.Runs, report.Reps)

	if len(report.Models) > 0 {
		names := make([]string, 0, len(report.Models))
		for name := range report.Models {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintf(w, "Models: %s\n\n", strings.Join(names, ", "))
	}

	fmt.Fprintf(w, "| Configuration | Solved | Pass rate | Reps agreed | Median steps | Median tokens | Tokens per solved task | Stops |\n")
	fmt.Fprintf(w, "| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, cfg := range report.Configurations {
		rate := 0.0
		if cfg.Runs > 0 {
			rate = float64(cfg.Passes) / float64(cfg.Runs)
		}
		fmt.Fprintf(w, "| `%s` | %d/%d | %.0f%% | %.0f%% | %d | %d | %d | %s |\n",
			cfg.Name, cfg.Solved, cfg.TaskCount, rate*100, cfg.Agreement*100,
			cfg.MedianSteps, cfg.MedianTokens, cfg.TokensPerSolved, stopsText(cfg.Stops))
	}

	fmt.Fprintf(w, "\n**Verdict.** ")
	if report.Verdict.Met {
		fmt.Fprintf(w, "The signal moves — %s. An evaluation framework has something to measure; whether it should be built is a separate argument, and the task set's own gradient is what it turns on.\n", report.Verdict.Detail)
	} else if !report.Verdict.Complete {
		fmt.Fprintf(w, "Not decided — %s.\n", report.Verdict.Detail)
	} else {
		fmt.Fprintf(w, "**The abandonment condition is met, against building the framework.** %s. Either the task set is too easy, the harness measures the wrong thing, or the noise is wider than the difference; what gets built next is a harder task set, not a framework.\n", report.Verdict.Detail)
	}

	fmt.Fprintf(w, "\n## Per task\n\n| Task | %s |\n| --- |", strings.Join(columnNames(report.Configurations), " | "))
	fmt.Fprintf(w, "%s\n", strings.Repeat(" --- |", len(report.Configurations)))
	for index, cell := range firstConfiguration(report.Configurations).Tasks {
		fmt.Fprintf(w, "| `%s` |", cell.Task)
		for _, cfg := range report.Configurations {
			if index >= len(cfg.Tasks) {
				fmt.Fprintf(w, " — |")
				continue
			}
			got := cfg.Tasks[index]
			mark := fmt.Sprintf("%d/%d", got.Passes, got.Reps)
			if got.Tampered > 0 {
				mark += fmt.Sprintf(" (%d tampered)", got.Tampered)
			}
			fmt.Fprintf(w, " %s |", mark)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprintf(w, "\n## What the numbers are\n\n")
	fmt.Fprintf(w, "- **Solved** counts tasks where a majority of repetitions passed, graded by the task's own `go test ./...`.\n")
	fmt.Fprintf(w, "- **Reps agreed** is the fraction of tasks where every repetition reached the same verdict: the noise floor, not a confidence interval.\n")
	fmt.Fprintf(w, "- **Tokens per solved task** is the whole configuration's token spend divided by its solved tasks, including what the failed attempts burned.\n")
	fmt.Fprintf(w, "- A run whose test files were edited counts as a failure and is marked `tampered`: a pass obtained by weakening the test is not a pass.\n")
	fmt.Fprintf(w, "- Metrics come from the run's trajectory (`agent.ListRuns`), not from this runner's own bookkeeping.\n")
}

// stopsText renders the stop vocabulary in a fixed order, so a report diff is
// readable.
func stopsText(stops map[string]int) string {
	if len(stops) == 0 {
		return "—"
	}
	order := []string{"final", "budget", "blocked", "step_limit", "cancelled", "error"}
	var parts []string
	seen := make(map[string]bool)
	for _, name := range order {
		if count, ok := stops[name]; ok && count > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", name, count))
			seen[name] = true
		}
	}
	rest := make([]string, 0)
	for name, count := range stops {
		if !seen[name] && count > 0 {
			rest = append(rest, fmt.Sprintf("%s %d", name, count))
		}
	}
	sort.Strings(rest)
	parts = append(parts, rest...)
	return strings.Join(parts, ", ")
}

func columnNames(configs []configSummary) []string {
	names := make([]string, 0, len(configs))
	for _, cfg := range configs {
		names = append(names, "`"+cfg.Name+"`")
	}
	return names
}

func firstConfiguration(configs []configSummary) configSummary {
	if len(configs) == 0 {
		return configSummary{}
	}
	return configs[0]
}
