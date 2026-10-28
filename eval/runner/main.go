// Command runner measures whether an evaluation signal exists at all.
//
// This is a spike, not a framework (issue #252). It runs a small task set
// through a few deliberately different agent configurations, grades every
// attempt by the task's own `go test ./...` exit code, and reports the numbers
// from each run's trajectory. The question it answers is not "is the agent good"
// but "does this measurement separate configurations that must separate" — and
// if it does not, the honest answer is to stop here.
//
// It spends money, so it is gated: TINYCODE_EVAL=1 is required, and nothing in
// CI sets it.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/types"
)

// configuration is one agent set-up to measure. They are chosen so the answer to
// "does the signal move" is not in doubt: build should do well, a starved budget
// should stop mid-task, and the read-only agent cannot write at all.
type configuration struct {
	Name string
	// Mode is the default_mode written into the work copy's config.
	Mode string
	// MaxTokensPerRun is the hard budget for one attempt; 0 leaves the default.
	MaxTokensPerRun int
	// RepoMap prepends a symbol digest of the work copy to the prompt. It is
	// the treatment arm of the repository-index spike (roadmap B4): no new
	// capability to call, just the map in hand.
	RepoMap bool
}

var configurations = []configuration{
	{Name: "build", Mode: "build"},
	{Name: "limited", Mode: "build", MaxTokensPerRun: 4000},
	{Name: "plan", Mode: "plan"},
}

// mapConfiguration is the spike's treatment arm. It is deliberately not in the
// default three: a run measures it only when -configurations names it, so the
// committed baseline keeps describing the same experiment it always did.
var mapConfiguration = configuration{Name: "map", Mode: "build", RepoMap: true}

// task is one directory under eval/tasks.
type task struct {
	Name   string
	Dir    string
	Prompt string
	// Tier is the declared difficulty, read from the task's TIER file: 1 is one
	// file and an obvious defect, 2 is several files and a contract between them,
	// 3 is a prompt that names only the symptom and a suite that is the spec.
	Tier int
	// Tests is the digest of the task's test files, so an attempt that edited
	// them can be told apart from one that passed honestly.
	Tests map[string]string
}

// runResult is one attempt.
type runResult struct {
	Configuration string           `json:"configuration"`
	Task          string           `json:"task"`
	Rep           int              `json:"rep"`
	Pass          bool             `json:"pass"`
	TestsModified bool             `json:"tests_modified"`
	ExitCode      int              `json:"exit_code"`
	WallMS        int64            `json:"wall_ms"`
	Model         string           `json:"model,omitempty"`
	Tier          int              `json:"tier,omitempty"`
	Stop          string           `json:"stop,omitempty"`
	Steps         int              `json:"steps"`
	Tokens        int              `json:"tokens"`
	Cost          types.CostTotals `json:"cost,omitempty"`
	Tools         map[string]int   `json:"tools,omitempty"`
	TrajectoryRun string           `json:"trajectory_run,omitempty"`
	RecordDir     string           `json:"record_dir"`
	WorkDir       string           `json:"work_dir"`
	Log           string           `json:"log"`
	Note          string           `json:"note,omitempty"`
}

func main() {
	tasksDir := flag.String("tasks", "eval/tasks", "directory of task directories")
	reps := flag.Int("reps", 2, "attempts per (configuration x task)")
	binary := flag.String("binary", "bin/tinycode", "the binary under test")
	out := flag.String("out", "eval", "where baseline.json and BASELINE.md are written")
	timeout := flag.Duration("timeout", 180*time.Second, "wall-clock bound for one attempt")
	work := flag.String("work", "", "scratch directory (default: a fresh temporary one)")
	only := flag.String("configurations", "", "comma-separated subset of the configurations")
	hidden := flag.String("hidden", "", "directory of hidden tests to install after each attempt (one subdirectory per task); empty grades the work copy as it is")
	render := flag.Bool("render", false, "re-render <out>/BASELINE.md from <out>/baseline.json, running nothing")
	limitedBudget := flag.Int("limited-budget", configurations[1].MaxTokensPerRun,
		"token budget for the `limited` configuration; a middle rung is only a gradient if it is above the cliff")
	flag.Parse()
	for index := range configurations {
		if configurations[index].Name == "limited" {
			configurations[index].MaxTokensPerRun = *limitedBudget
		}
	}

	// Re-rendering spends nothing, so it is not gated: the report's wording must
	// be fixable without paying to regenerate the data behind it.
	if *render {
		if err := renderBaseline(*out); err != nil {
			fail(err)
		}
		fmt.Printf("re-rendered %s/BASELINE.md from %s/baseline.json\n", *out, *out)
		return
	}

	if os.Getenv("TINYCODE_EVAL") != "1" {
		fmt.Println("runner: this spends money and is gated.")
		fmt.Println("  TINYCODE_EVAL=1 go run ./eval/runner -reps 2")
		os.Exit(0)
	}

	absBinary, err := filepath.Abs(*binary)
	if err != nil {
		fail(err)
	}
	if _, err := os.Stat(absBinary); err != nil {
		fail(fmt.Errorf("no binary at %s: run `make build` first: %w", absBinary, err))
	}

	var tasks []task
	if *hidden != "" {
		tasks, err = loadSpikeTasks(*tasksDir, *hidden)
	} else {
		tasks, err = loadTasks(*tasksDir)
	}
	if err != nil {
		fail(err)
	}
	configs := configurations
	if *only != "" {
		configs, err = selectConfigurations(*only)
		if err != nil {
			fail(err)
		}
	}

	scratch := *work
	if scratch == "" {
		scratch, err = os.MkdirTemp("", "tinycode-eval-")
		if err != nil {
			fail(err)
		}
		defer os.RemoveAll(scratch)
	}
	fmt.Printf("evaluating %d tasks x %d configurations x %d reps = %d runs\n",
		len(tasks), len(configs), *reps, len(tasks)*len(configs)*(*reps))
	fmt.Printf("binary: %s\nscratch: %s\n\n", absBinary, scratch)

	var results []runResult
	for _, cfg := range configs {
		fmt.Printf("== %s\n", cfg.Name)
		for _, tk := range tasks {
			line := fmt.Sprintf("  %-18s ", tk.Name)
			for rep := 0; rep < *reps; rep++ {
				result, err := attempt(scratch, absBinary, cfg, tk, rep, *timeout, *hidden)
				if err != nil {
					fail(err)
				}
				results = append(results, result)
				mark := "fail"
				if result.Pass {
					mark = "pass"
				}
				if result.TestsModified {
					mark += "+tampered"
				}
				line += mark + " "
			}
			fmt.Println(line)
		}
	}

	report, err := summarise(results, configs)
	if err != nil {
		fail(err)
	}
	if err := writeBaseline(*out, report); err != nil {
		fail(err)
	}
	fmt.Println()
	printMarkdown(os.Stdout, report)
}

// attempt runs the agent once against one task and grades the result.
func attempt(scratch, binary string, cfg configuration, tk task, rep int, timeout time.Duration, hiddenDir string) (runResult, error) {
	base := filepath.Join(scratch, cfg.Name, tk.Name, fmt.Sprintf("rep%d", rep))
	workDir := filepath.Join(base, "work")
	recordDir := filepath.Join(base, "records")
	logPath := filepath.Join(base, "run.log")
	if err := os.MkdirAll(recordDir, 0o755); err != nil {
		return runResult{}, err
	}
	if err := copyTask(tk, workDir); err != nil {
		return runResult{}, err
	}

	// The work copy's own config carries the three things the attempt needs and
	// nothing else: the agent mode under test, the budget that must stop a
	// starved run during the run rather than after it, and where this attempt's
	// record goes. The agent cannot write the record directory: it is the
	// reserved path the tool layer refuses.
	if err := writeWorkConfig(workDir, recordDir, cfg); err != nil {
		return runResult{}, err
	}

	result := runResult{
		Configuration: cfg.Name,
		Task:          tk.Name,
		Rep:           rep,
		RecordDir:     recordDir,
		WorkDir:       workDir,
		Log:           logPath,
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, promptFor(cfg, tk, workDir))
	cmd.Dir = workDir
	logFile, err := os.Create(logPath)
	if err != nil {
		return runResult{}, err
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	started := time.Now()
	runErr := cmd.Run()
	result.WallMS = time.Since(started).Milliseconds()
	_ = logFile.Close()
	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		} else {
			return runResult{}, fmt.Errorf("run %s/%s/rep%d: %w", cfg.Name, tk.Name, rep, runErr)
		}
	}
	if ctx.Err() != nil {
		result.Note = "the attempt hit the runner's wall-clock bound"
	}

	// The record first: it is what a missing file has to be reported from.
	summaries, err := agent.ListRuns(recordDir, 0)
	if err != nil {
		return runResult{}, err
	}
	switch len(summaries) {
	case 1:
		run := summaries[0]
		result.Model = run.Model
		result.Tier = tk.Tier
		result.Stop = run.Stop
		result.Steps = run.Steps
		result.Tokens = run.TokensTotal()
		result.Cost = run.Cost
		result.Tools = run.Tools
		result.TrajectoryRun = run.Run
		if !run.Complete {
			result.Note = "the run left no run.end"
		}
	case 0:
		result.Note = "the run left no trajectory"
	default:
		result.Note = fmt.Sprintf("the run left %d trajectories", len(summaries))
	}

	// A hidden suite is installed after the run, so the agent never saw the
	// assertions it is graded by: that is what makes the task measure finding
	// the defect rather than reading the test's file name.
	if hiddenDir != "" {
		if err := installHiddenTests(hiddenDir, tk.Name, workDir); err != nil {
			return runResult{}, err
		}
	}

	// Graded by the task's own test command, in the copy the agent was given.
	result.Pass = gradeTask(workDir)
	modified, err := testsModified(tk, workDir)
	if err != nil {
		return runResult{}, err
	}
	result.TestsModified = modified
	if modified {
		// A pass obtained by weakening the test is not a pass. The attempt stays
		// in the data with both facts, so the report can count them separately.
		result.Pass = false
	}
	return result, nil
}

// copyTree copies a task subdirectory into the work copy, skipping the files
// that describe the task rather than the code (PROMPT, and TIER where it sits).
func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		from := filepath.Join(src, entry.Name())
		to := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := copyTree(from, to); err != nil {
				return err
			}
			continue
		}
		if entry.Name() == "PROMPT" || entry.Name() == "TIER" {
			continue
		}
		data, err := os.ReadFile(from)
		if err != nil {
			return err
		}
		if err := os.WriteFile(to, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// gradeTask runs the task's own command and reports whether it passed.
func gradeTask(dir string) bool {
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true
	}
	_ = out
	return false
}

// loadTasks reads every task directory: a go.mod, a PROMPT, and at least one
// test file.
func loadTasks(dir string) ([]task, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read tasks: %w", err)
	}
	var tasks []task
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		promptFile := filepath.Join(path, "PROMPT")
		prompt, err := os.ReadFile(promptFile)
		if err != nil {
			return nil, fmt.Errorf("task %s has no PROMPT: %w", entry.Name(), err)
		}
		if _, err := os.Stat(filepath.Join(path, "go.mod")); err != nil {
			return nil, fmt.Errorf("task %s has no go.mod: %w", entry.Name(), err)
		}
		tier, err := readTier(path)
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", entry.Name(), err)
		}
		digests, err := testDigests(path)
		if err != nil {
			return nil, err
		}
		if len(digests) == 0 {
			return nil, fmt.Errorf("task %s has no test file", entry.Name())
		}
		tasks = append(tasks, task{
			Name:   entry.Name(),
			Dir:    path,
			Tier:   tier,
			Prompt: strings.TrimSpace(string(prompt)),
			Tests:  digests,
		})
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no tasks in %s", dir)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].Name < tasks[j].Name })
	return tasks, nil
}

// testDigests hashes every test file of a task, keyed by base name.
func testDigests(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	digests := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		digests[entry.Name()] = digest(data)
	}
	return digests, nil
}

// testsModified reports whether any test file in the work copy differs from the
// task's own.
func testsModified(tk task, workDir string) (bool, error) {
	digests, err := testDigests(workDir)
	if err != nil {
		// A deleted test file is a modification, not an error.
		return true, nil
	}
	if len(digests) != len(tk.Tests) {
		return true, nil
	}
	for name, want := range tk.Tests {
		if digests[name] != want {
			return true, nil
		}
	}
	return false, nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// copyTask copies the task's files into a fresh work directory.
func copyTask(tk task, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	src := tk.Dir
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			// A task may carry subpackages: the spike's tasks pad their tree so
			// that finding the defect is work, and the padding has to reach the
			// work copy or the treatment measures nothing.
			if err := copyTree(filepath.Join(src, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
			continue
		}
		if entry.Name() == "PROMPT" {
			continue
		}
		in, err := os.Open(filepath.Join(src, entry.Name()))
		if err != nil {
			return err
		}
		out, err := os.Create(filepath.Join(dir, entry.Name()))
		if err != nil {
			in.Close()
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			in.Close()
			out.Close()
			return err
		}
		in.Close()
		if err := out.Close(); err != nil {
			return err
		}
	}
	return nil
}

// writeWorkConfig puts the attempt's configuration in the copy's own layer.
func writeWorkConfig(workDir, recordDir string, cfg configuration) error {
	type budget struct {
		MaxTokensPerRun int `json:"max_tokens_per_run,omitempty"`
	}
	type trajectory struct {
		Dir string `json:"dir"`
	}
	document := struct {
		DefaultMode string     `json:"default_mode"`
		LogLevel    string     `json:"log_level"`
		Budget      *budget    `json:"budget,omitempty"`
		Trajectory  trajectory `json:"trajectory"`
	}{
		DefaultMode: cfg.Mode,
		LogLevel:    "warn",
		Trajectory:  trajectory{Dir: recordDir},
	}
	if cfg.MaxTokensPerRun > 0 {
		document.Budget = &budget{MaxTokensPerRun: cfg.MaxTokensPerRun}
	}
	dir := filepath.Join(workDir, ".tinycode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), append(data, '\n'), 0o600)
}

func selectConfigurations(list string) ([]configuration, error) {
	var chosen []configuration
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		found := false
		pool := configurations
		if mapConfiguration.Name == name {
			pool = []configuration{mapConfiguration}
		}
		for _, cfg := range pool {
			if cfg.Name == name {
				chosen = append(chosen, cfg)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown configuration %q", name)
		}
	}
	return chosen, nil
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "runner: %v\n", err)
	os.Exit(1)
}

// renderBaseline rewrites the Markdown report from the committed data, so a
// wording fix does not require spending money to regenerate the numbers.
func renderBaseline(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "baseline.json"))
	if err != nil {
		return err
	}
	var report baselineReport
	if err := json.Unmarshal(data, &report); err != nil {
		return err
	}
	// The verdict is derived from the stored summaries rather than trusted from
	// the file: the rule and its wording live in code, and a render must not
	// reproduce a sentence the code no longer says. The numbers are untouched.
	report.Verdict = judge(report.Configurations)
	rendered, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "baseline.json"), append(rendered, '\n'), 0o644); err != nil {
		return err
	}
	var markdown strings.Builder
	printMarkdown(&markdown, report)
	return os.WriteFile(filepath.Join(dir, "BASELINE.md"), []byte(markdown.String()), 0o644)
}

// readTier reads a task's declared difficulty.
//
// It is declared rather than inferred: a task's tier decides what a gradient
// claim means, and inferring it from the file count would quietly relabel a hard
// single-file task as easy.
func readTier(dir string) (int, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "TIER"))
	if err != nil {
		return 0, fmt.Errorf("no TIER file: %w", err)
	}
	tier, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return 0, fmt.Errorf("TIER is not a number: %w", err)
	}
	if tier < 1 || tier > 3 {
		return 0, fmt.Errorf("TIER %d is outside the declared tiers 1-3", tier)
	}
	return tier, nil
}
