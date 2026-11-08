package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeVariants is the fixture helper: a variants file in a temp dir.
func writeVariants(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "variants.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoadVariantsReadsAPromptFromAFile is the shape an experiment actually uses:
// the prompt lives in a file next to the run, not inside the JSON.
func TestLoadVariantsReadsAPromptFromAFile(t *testing.T) {
	prompt := filepath.Join(t.TempDir(), "prompt-b.txt")
	if err := os.WriteFile(prompt, []byte("You are variant B."), 0o644); err != nil {
		t.Fatal(err)
	}
	path := writeVariants(t, `[
		{"name": "a", "mode": "build"},
		{"name": "b", "mode": "plan", "system_prompt_file": `+strconv_quote(prompt)+`, "model": "deepseek/deepseek-chat", "max_steps": 40, "max_tokens_per_run": 9000}
	]`)

	configs, err := loadVariants(path)
	if err != nil {
		t.Fatalf("loadVariants: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("configs = %d", len(configs))
	}
	if configs[0].Name != "a" || configs[0].Mode != "build" {
		t.Fatalf("a = %+v", configs[0])
	}
	b := configs[1]
	if b.Name != "b" || b.Mode != "plan" || b.SystemPrompt != "You are variant B." ||
		b.Model != "deepseek/deepseek-chat" || b.MaxSteps != 40 || b.MaxTokensPerRun != 9000 {
		t.Fatalf("b = %+v", b)
	}
}

// TestLoadVariantsRefusesWhatAComparisonCannotUse: each refusal is a way to get an
// A/B result that means nothing.
func TestLoadVariantsRefusesWhatAComparisonCannotUse(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"one variant", `[{"name": "a"}]`, "at least two"},
		{"no name", `[{"mode": "build"}, {"name": "b"}]`, "no name"},
		{"duplicate name", `[{"name": "a"}, {"name": "a"}]`, "two variants are named"},
		{"unknown mode", `[{"name": "a", "mode": "review"}, {"name": "b"}]`, "use build or plan"},
		{"both prompts", `[{"name": "a", "system_prompt": "x", "system_prompt_file": "y"}, {"name": "b"}]`, "both system_prompt and system_prompt_file"},
		{"prompt file missing", `[{"name": "a", "system_prompt_file": "/tmp/does-not-exist-for-variants"}, {"name": "b"}]`, "no such file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadVariants(writeVariants(t, tc.body))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

// TestWorkConfigCarriesTheVariant: the overlay reaches the work copy as the same
// agent keys a user's own config uses, so an experiment cannot exercise a code
// path the product does not have.
func TestWorkConfigCarriesTheVariant(t *testing.T) {
	work := t.TempDir()
	cfg := configuration{
		Name: "b", Mode: "build",
		SystemPrompt: "You are variant B.", Model: "deepseek/deepseek-chat", MaxSteps: 40,
		MaxTokensPerRun: 9000,
	}
	if err := writeWorkConfig(work, filepath.Join(work, "records"), cfg); err != nil {
		t.Fatalf("writeWorkConfig: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(work, ".tinycode", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"default_mode": "build"`,
		`"system_prompt": "You are variant B."`,
		`"model": "deepseek/deepseek-chat"`,
		`"max_steps": 40`,
		`"max_tokens_per_run": 9000`,
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("config is missing %s:\n%s", want, data)
		}
	}

	// A configuration with no variant keys writes no agents block: the product's
	// own agents are used, which is what makes the baseline a baseline.
	plain := t.TempDir()
	if err := writeWorkConfig(plain, filepath.Join(plain, "records"), configuration{Name: "build", Mode: "build"}); err != nil {
		t.Fatal(err)
	}
	plainData, err := os.ReadFile(filepath.Join(plain, ".tinycode", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plainData), `"agents"`) {
		t.Fatalf("a plain configuration wrote an agents block:\n%s", plainData)
	}
}

// TestCompareVariantsIsDominanceOnly: the rule is dominance, not a mean, and a
// trade — one solve for one loss — is inconclusive.
func TestCompareVariantsIsDominanceOnly(t *testing.T) {
	report := baselineReport{Configurations: []configSummary{
		{Name: "a", Tasks: []taskSummary{
			{Task: "shared", Solved: true},
			{Task: "a-only", Solved: true},
			{Task: "b-only", Solved: false},
		}},
		{Name: "b", Tasks: []taskSummary{
			{Task: "shared", Solved: true},
			{Task: "a-only", Solved: false},
			{Task: "b-only", Solved: true},
		}},
	}}
	comparisons := compareVariants(report)
	if len(comparisons) != 1 {
		t.Fatalf("comparisons = %d", len(comparisons))
	}
	got := comparisons[0]
	if got.Winner != "" {
		t.Fatalf("a trade produced a winner: %+v", got)
	}
	if len(got.OnlyA) != 1 || got.OnlyA[0] != "a-only" || len(got.OnlyB) != 1 || got.OnlyB[0] != "b-only" {
		t.Fatalf("differences = %+v", got)
	}

	// Dominance: b solves everything a does, plus one.
	report.Configurations[1].Tasks[1].Solved = true
	got = compareVariants(report)[0]
	if got.Winner != "b" {
		t.Fatalf("a dominating variant did not win: %+v", got)
	}
}

// TestABSectionIsRendered: the section is what a prompt-change review reads.
func TestABSectionIsRendered(t *testing.T) {
	report := baselineReport{Configurations: []configSummary{
		{Name: "a", Tasks: []taskSummary{{Task: "t", Solved: true}}},
		{Name: "b", Tasks: []taskSummary{{Task: "t", Solved: true}}},
	}}
	report.AB = compareVariants(report)
	var out strings.Builder
	printMarkdown(&out, report)
	if !strings.Contains(out.String(), "## A/B comparison") || !strings.Contains(out.String(), "solved the same tasks") {
		t.Fatalf("the section is missing:\n%s", out.String())
	}
}

// strconv_quote quotes a path for embedding in JSON.
func strconv_quote(path string) string {
	return strconv.Quote(path)
}
