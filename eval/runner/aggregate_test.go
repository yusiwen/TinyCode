package main

import (
	"strings"
	"testing"
)

// attemptIn builds a result, so the tests read as data rather than as literals.
func attemptIn(config, task string, rep int, pass bool, steps, tokens int, stop string) runResult {
	return runResult{
		Configuration: config,
		Task:          task,
		Rep:           rep,
		Pass:          pass,
		Steps:         steps,
		Tokens:        tokens,
		Stop:          stop,
		Model:         "test-model",
		Tools:         map[string]int{"bash": 1},
	}
}

// TestSummariseCountsSolvedTasksByMajority pins the unit the verdict is about: a
// task, not a repetition. Three flaky attempts on one task are one task, and a
// report that counted them as three would flatter a configuration that retries.
func TestSummariseCountsSolvedTasksByMajority(t *testing.T) {
	results := []runResult{
		attemptIn("build", "a", 0, true, 3, 100, "final"),
		attemptIn("build", "a", 1, false, 5, 200, "final"),
		attemptIn("build", "a", 2, true, 4, 150, "final"),
		attemptIn("build", "b", 0, false, 2, 50, "budget"),
		attemptIn("build", "b", 1, false, 2, 50, "budget"),
		attemptIn("build", "b", 2, false, 2, 50, "budget"),
		// A minority pass: one rep of three. This is the cell that separates
		// "solved by majority" from "solved at least once", and a report that
		// counted it would flatter a configuration that got lucky once.
		attemptIn("build", "c", 0, true, 9, 400, "final"),
		attemptIn("build", "c", 1, false, 9, 400, "budget"),
		attemptIn("build", "c", 2, false, 9, 400, "budget"),
	}
	report, err := summarise(results, []configuration{{Name: "build", Mode: "build"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Configurations) != 1 {
		t.Fatalf("want one configuration, got %d", len(report.Configurations))
	}
	cfg := report.Configurations[0]
	if cfg.Solved != 1 || cfg.TaskCount != 3 {
		t.Fatalf("solved = %d/%d, want 1/3", cfg.Solved, cfg.TaskCount)
	}
	if cfg.Passes != 3 || cfg.Runs != 9 {
		t.Fatalf("passes = %d/%d, want 3/9", cfg.Passes, cfg.Runs)
	}
	// Only task "b" agreed with itself on every rep: "a" split 2-1 and "c" split
	// 1-2, and a noise reading that forgave a split would report a stabler agent
	// than the one that ran.
	if got := cfg.Agreement; got < 0.33 || got > 0.34 {
		t.Fatalf("agreement = %v, want 1/3", got)
	}
	// Tokens per solved task counts everything spent, including the failures.
	if cfg.TokensPerSolved != (100+200+150+50+50+50+400+400+400)/1 {
		t.Fatalf("tokens per solved = %d", cfg.TokensPerSolved)
	}
	if cfg.Stops["budget"] != 5 || cfg.Stops["final"] != 4 {
		t.Fatalf("stops = %v, want budget 5 and final 4", cfg.Stops)
	}
	if report.Reps != 3 {
		t.Fatalf("reps = %d, want 3", report.Reps)
	}
	if report.Models["test-model"] != 9 {
		t.Fatalf("models = %v", report.Models)
	}
}

// TestJudgeAppliesTheAbandonmentCondition pins the rule that was written down
// before the data: strictly more solved tasks at every rung.
func TestJudgeAppliesTheAbandonmentCondition(t *testing.T) {
	cell := func(name string, solved int) configSummary {
		return configSummary{Name: name, Solved: solved, TaskCount: 6}
	}

	t.Run("moves", func(t *testing.T) {
		got := judge([]configSummary{cell("plan", 0), cell("build", 5), cell("limited", 2)})
		if !got.Met || !got.Complete {
			t.Fatalf("verdict = %+v, want met and complete", got)
		}
		if got.Order[0] != "build" || got.Order[1] != "limited" || got.Order[2] != "plan" {
			t.Fatalf("order = %v, want build, limited, plan", got.Order)
		}
	})

	t.Run("a tie is not a move", func(t *testing.T) {
		got := judge([]configSummary{cell("build", 3), cell("limited", 3), cell("plan", 0)})
		if got.Met {
			t.Fatalf("verdict = %+v, want not met: equal solved counts do not separate", got)
		}
	})

	// The floor case, and the reason the condition was corrected before any data
	// existed: a read-only agent solving nothing is expected, and a rule phrased
	// "strictly more than plan" would fail on 0 > 0 for an arithmetic reason.
	t.Run("zero is the expected floor", func(t *testing.T) {
		got := judge([]configSummary{cell("build", 4), cell("limited", 0), cell("plan", 0)})
		if !got.Met {
			t.Fatalf("verdict = %+v, want met: build separated and plan did not beat limited", got)
		}
	})

	t.Run("the read-only rung must not win", func(t *testing.T) {
		got := judge([]configSummary{cell("build", 4), cell("limited", 1), cell("plan", 2)})
		if got.Met {
			t.Fatalf("verdict = %+v, want not met when plan beats limited", got)
		}
	})

	t.Run("a floor is not a signal", func(t *testing.T) {
		got := judge([]configSummary{cell("build", 1), cell("limited", 0), cell("plan", 0)})
		if got.Met {
			t.Fatalf("verdict = %+v, want not met when the agent solves under half the set", got)
		}
	})

	t.Run("an incomplete run decides nothing", func(t *testing.T) {
		got := judge([]configSummary{cell("build", 4)})
		if got.Met || got.Complete {
			t.Fatalf("verdict = %+v, want incomplete", got)
		}
	})
}

// TestMedianIntIsDeterministic: the report prints a median on every row, so the
// same data must always produce the same digit.
func TestMedianIntIsDeterministic(t *testing.T) {
	cases := []struct {
		in   []int
		want int
	}{
		{nil, 0},
		{[]int{7}, 7},
		{[]int{3, 1, 2}, 2},
		{[]int{4, 1, 3, 2}, 3}, // upper median
	}
	for _, tc := range cases {
		if got := medianInt(tc.in); got != tc.want {
			t.Errorf("medianInt(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// TestATamperedAttemptCountsAsAFailure keeps the integrity rule visible in the
// data: a pass obtained by editing the test is recorded, counted, and not solved.
func TestATamperedAttemptCountsAsAFailure(t *testing.T) {
	// The runner sets Pass=false when it detects a modification; this pins that
	// the aggregation does not put the pass back.
	result := attemptIn("build", "a", 0, false, 1, 10, "final")
	cell := summariseTask("a", []runResult{result})
	if cell.Solved || cell.Passes != 0 {
		t.Fatalf("cell = %+v, want it unsolved", cell)
	}
	result.TestsModified = true
	cell = summariseTask("a", []runResult{result})
	if cell.Tampered != 1 {
		t.Fatalf("tampered = %d, want 1", cell.Tampered)
	}
}

// TestStopsTextIsOrdered keeps a report diff readable: the same stops always
// print in the same order.
func TestStopsTextIsOrdered(t *testing.T) {
	got := stopsText(map[string]int{"error": 1, "final": 3, "budget": 2})
	if got != "final 3, budget 2, error 1" {
		t.Fatalf("stops = %q", got)
	}
	if stopsText(nil) != "—" {
		t.Fatalf("empty stops = %q", stopsText(nil))
	}
}

// TestMarkdownStatesTheVerdict is the report's contract: the verdict paragraph
// must not be ambiguous, in either direction.
func TestMarkdownStatesTheVerdict(t *testing.T) {
	results := []runResult{
		attemptIn("build", "a", 0, true, 1, 10, "final"),
		attemptIn("limited", "a", 0, false, 1, 10, "budget"),
		attemptIn("plan", "a", 0, false, 0, 0, "final"),
	}
	report, err := summarise(results, []configuration{
		{Name: "build"}, {Name: "limited", MaxTokensPerRun: 4000}, {Name: "plan"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	printMarkdown(&out, report)
	text := out.String()
	if !report.Verdict.Met {
		t.Fatalf("verdict = %+v, want met", report.Verdict)
	}
	if !strings.Contains(text, "The signal moves") {
		t.Fatalf("report does not state the verdict:\n%s", text)
	}
	if !strings.Contains(text, "| Configuration | Solved |") {
		t.Fatalf("report has no summary table:\n%s", text)
	}
	if !strings.Contains(text, "tampered") {
		t.Fatalf("report does not explain the tampered rule:\n%s", text)
	}
}

// TestSelectConfigurationsRefusesAName: a typo must not silently run nothing.
func TestSelectConfigurationsRefusesAName(t *testing.T) {
	if _, err := selectConfigurations("build,nope"); err == nil {
		t.Fatal("an unknown configuration name was accepted")
	}
	got, err := selectConfigurations("build,plan")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "build" || got[1].Name != "plan" {
		t.Fatalf("selected %+v", got)
	}
}
