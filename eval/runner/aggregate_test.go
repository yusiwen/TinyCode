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

// TestTiersAreFoldedAndTheGradientJudged pins this increment's condition: build
// must solve a strictly smaller *fraction* of the hardest tier than of the
// easiest, which is not the same as a smaller count — the tiers do not hold the
// same number of tasks.
func TestTiersAreFoldedAndTheGradientJudged(t *testing.T) {
	tiered := func(taskName string, tier int, pass bool) runResult {
		result := attemptIn("build", taskName, 0, pass, 3, 100, "final")
		result.Tier = tier
		return result
	}

	results := []runResult{
		tiered("easy-a", 1, true), tiered("easy-b", 1, true), tiered("easy-c", 1, true),
		tiered("mid-a", 2, true), tiered("mid-b", 2, false), tiered("mid-c", 2, true),
		tiered("hard-a", 3, false), tiered("hard-b", 3, false), tiered("hard-c", 3, false),
	}
	report, err := summarise(results, []configuration{{Name: "build"}})
	if err != nil {
		t.Fatal(err)
	}
	build := report.Configurations[0]
	byTier := make(map[int]tierSummary)
	for _, row := range build.Tiers {
		byTier[row.Tier] = row
	}
	if byTier[1].Solved != 3 || byTier[1].Tasks != 3 {
		t.Fatalf("tier 1 = %+v, want 3/3", byTier[1])
	}
	if byTier[2].Solved != 2 || byTier[2].Tasks != 3 {
		t.Fatalf("tier 2 = %+v, want 2/3", byTier[2])
	}
	if byTier[3].Solved != 0 || byTier[3].Tasks != 3 {
		t.Fatalf("tier 3 = %+v, want 0/3", byTier[3])
	}
	// The tier's cost is part of the gradient: a tier that costs more to solve is
	// a different kind of difficulty from one that simply fails more often.
	if byTier[1].TokensPerSolved != 100 {
		t.Fatalf("tier 1 tokens per solved = %d, want 100: three tasks of 100 tokens, all solved", byTier[1].TokensPerSolved)
	}
	if !report.Gradient.Exist {
		t.Fatalf("gradient = %+v, want it to exist: 0/3 is below 3/3", report.Gradient)
	}
	if !report.Gradient.Complete {
		t.Fatalf("gradient = %+v, want complete", report.Gradient)
	}

	t.Run("equal fractions are not a gradient", func(t *testing.T) {
		flat := []runResult{
			tiered("easy-a", 1, true), tiered("easy-b", 1, true), tiered("easy-c", 1, true),
			tiered("hard-a", 3, true),
		}
		report, err := summarise(flat, []configuration{{Name: "build"}})
		if err != nil {
			t.Fatal(err)
		}
		if report.Gradient.Exist {
			t.Fatalf("gradient = %+v, want none: 3/3 and 1/1 are the same fraction", report.Gradient)
		}
	})

	t.Run("a missing tier decides nothing", func(t *testing.T) {
		onlyEasy := []runResult{tiered("easy-a", 1, true)}
		report, err := summarise(onlyEasy, []configuration{{Name: "build"}})
		if err != nil {
			t.Fatal(err)
		}
		if report.Gradient.Complete || report.Gradient.Exist {
			t.Fatalf("gradient = %+v, want undecided without a tier 3", report.Gradient)
		}
	})
}

// TestTheReportShowsTheGradient: the numbers have to be in the document, not
// derivable from it by hand.
func TestTheReportShowsTheGradient(t *testing.T) {
	make1 := func(name string, tier int, pass bool) runResult {
		result := attemptIn("build", name, 0, pass, 2, 50, "final")
		result.Tier = tier
		return result
	}
	results := []runResult{
		make1("easy-a", 1, true), make1("easy-b", 1, true),
		make1("hard-a", 3, false), make1("hard-b", 3, false),
	}
	report, err := summarise(results, []configuration{{Name: "build"}})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	printMarkdown(&out, report)
	text := out.String()
	for _, want := range []string{"## The gradient", "| Tier |", "Tokens per solved task", "**Gradient.**"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report is missing %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "(t1)") || !strings.Contains(text, "(t3)") {
		t.Errorf("the per-task table does not name the tiers:\n%s", text)
	}
}

// TestHeadroomNeedsBothHalves pins the third increment's condition: a set with no
// failing task has no headroom, and a set with no passing task is a wall.
func TestHeadroomNeedsBothHalves(t *testing.T) {
	cell := func(name string, pass bool) runResult {
		result := attemptIn("build", name, 0, pass, 3, 100, "final")
		result.Tier = 3
		return result
	}

	t.Run("one failure among successes has headroom", func(t *testing.T) {
		report, err := summarise([]runResult{cell("a", true), cell("b", false)}, []configuration{{Name: "build"}})
		if err != nil {
			t.Fatal(err)
		}
		if !report.Headroom.Met || !report.Headroom.Complete {
			t.Fatalf("headroom = %+v, want met", report.Headroom)
		}
		if len(report.Headroom.Failed) != 1 || report.Headroom.Failed[0] != "b" {
			t.Fatalf("failed = %v, want [b]", report.Headroom.Failed)
		}
	})

	t.Run("everything solved has none", func(t *testing.T) {
		report, err := summarise([]runResult{cell("a", true), cell("b", true)}, []configuration{{Name: "build"}})
		if err != nil {
			t.Fatal(err)
		}
		if report.Headroom.Met {
			t.Fatalf("headroom = %+v, want none: nothing defeated the agent", report.Headroom)
		}
	})

	t.Run("nothing solved is a wall", func(t *testing.T) {
		report, err := summarise([]runResult{cell("a", false), cell("b", false)}, []configuration{{Name: "build"}})
		if err != nil {
			t.Fatal(err)
		}
		if report.Headroom.Met {
			t.Fatalf("headroom = %+v, want none: a wall is not headroom", report.Headroom)
		}
	})
}

// TestDifficultyIsMeasuredFromTheRuns pins what the third increment changed: the
// order comes from the tokens the default configuration spent, not from the
// declared tier.
func TestDifficultyIsMeasuredFromTheRuns(t *testing.T) {
	makeRun := func(name string, tier, tokens int) runResult {
		result := attemptIn("build", name, 0, true, 3, tokens, "final")
		result.Tier = tier
		return result
	}
	results := []runResult{
		makeRun("cheap", 3, 10),
		makeRun("medium", 3, 100),
		makeRun("expensive", 1, 1000),
	}
	report, err := summarise(results, []configuration{{Name: "build"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Difficulty) != 3 {
		t.Fatalf("difficulty rows = %d, want 3", len(report.Difficulty))
	}
	// The declared tier says "expensive" should be tier 1 and the other two tier
	// 3; the measurement says the opposite, and the measurement is what orders it.
	if report.Difficulty[0].Task != "expensive" || report.Difficulty[0].MeasuredBand != "expensive" {
		t.Fatalf("first row = %+v, want the costliest task banded expensive", report.Difficulty[0])
	}
	if report.Difficulty[2].Task != "cheap" || report.Difficulty[2].MeasuredBand != "cheap" {
		t.Fatalf("last row = %+v, want the cheapest task", report.Difficulty[2])
	}
	if report.Difficulty[0].DeclaredTier != 1 {
		t.Fatalf("declared tier = %d, want the label to survive alongside the measurement", report.Difficulty[0].DeclaredTier)
	}
	// And the report has to show it.
	var out strings.Builder
	printMarkdown(&out, report)
	text := out.String()
	for _, want := range []string{"## Headroom", "## Difficulty, measured", "| Task | Declared tier | Measured band |"} {
		if !strings.Contains(text, want) {
			t.Errorf("the report is missing %q:\n%s", want, text)
		}
	}
}
