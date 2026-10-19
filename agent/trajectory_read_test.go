package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// writeTrajectoryFile writes a hand-made file, so the reader is tested against
// the format rather than against the writer it ships with. A schema both sides
// agree on by construction can drift together.
func writeTrajectoryFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const finishedRun = `{"v":1,"type":"run.start","run":"20261018T070400Z-r1","session":"S-9","agent":"build","provider":"testroute","model":"testmodel","capture":"metadata","at":"2026-10-18T07:04:00.5Z"}
{"v":1,"type":"llm.call","run":"20261018T070400Z-r1","step":0,"duration_ms":1200,"outcome":"tool_calls","at":"2026-10-18T07:04:01Z"}
{"v":1,"type":"run.end","run":"20261018T070400Z-r1","stop":"final","steps":2,"duration_ms":7400,"tokens":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120},"cost":[{"unit":"USD","amount":0.000231}],"at":"2026-10-18T07:04:07Z"}
`

// TestListRunsReadsTheFormatNotTheWriter is the reason this reader exists: a
// person or a script parses these files, and the list command is the first such
// consumer, so a field the writer stopped carrying fails here.
func TestListRunsReadsTheFormatNotTheWriter(t *testing.T) {
	dir := t.TempDir()
	writeTrajectoryFile(t, dir, "20261018T070400Z-r1.jsonl", finishedRun)

	runs, err := ListRuns(dir, 0)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("want one run, got %d", len(runs))
	}
	run := runs[0]
	if run.Run != "20261018T070400Z-r1" {
		t.Fatalf("run = %q", run.Run)
	}
	if !run.Complete || run.Stop != "final" || run.Steps != 2 {
		t.Fatalf("summary = %+v, want a complete final run with two steps", run)
	}
	if run.Duration != 7400*time.Millisecond {
		t.Fatalf("duration = %v", run.Duration)
	}
	if run.TokensTotal() != 120 {
		t.Fatalf("tokens = %d, want 120", run.TokensTotal())
	}
	if run.Cost["USD"] != 0.000231 {
		t.Fatalf("cost = %v", run.Cost)
	}
	if run.Session != "S-9" || run.Provider != "testroute" || run.Model != "testmodel" {
		t.Fatalf("identity fields = %+v", run)
	}
	if run.Lines != 3 {
		t.Fatalf("lines = %d, want 3", run.Lines)
	}
}

// TestListRunsReportsAnInterruptedRunAsIncomplete: a killed run leaves no
// run.end, and that is a fact about the run rather than a broken file.
func TestListRunsReportsAnInterruptedRunAsIncomplete(t *testing.T) {
	dir := t.TempDir()
	body := strings.Join(strings.Split(finishedRun, "\n")[:2], "\n") + "\n"
	writeTrajectoryFile(t, dir, "20261018T070400Z-r1.jsonl", body)

	runs, err := ListRuns(dir, 0)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("want one run, got %d", len(runs))
	}
	if runs[0].Complete {
		t.Fatal("a file with no run.end was reported as complete")
	}
	if runs[0].Stop != "" {
		t.Fatalf("stop = %q, want empty for an interrupted run", runs[0].Stop)
	}
}

// TestListRunsIsNewestFirstWithALimit pins the order the store already has, and
// the limit the command uses.
func TestListRunsIsNewestFirstWithALimit(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"20261018T070400Z-r1.jsonl",
		"20261019T070400Z-r1.jsonl",
		"20261020T070400Z-r1.jsonl",
	} {
		writeTrajectoryFile(t, dir, name, finishedRun)
	}
	all, err := ListRuns(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("want three runs, got %d", len(all))
	}
	if !strings.HasPrefix(all[0].Path, filepath.Join(dir, "20261020")) {
		t.Fatalf("first run is %q, want the newest", all[0].Path)
	}
	limited, err := ListRuns(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 || limited[0].Run != all[0].Run {
		t.Fatalf("limit = %d runs starting at %q", len(limited), limited[0].Run)
	}
}

// TestListRunsToleratesAHalfWrittenLine: the file is appended to by a process
// that can be killed, so a truncated last line is the normal shape of an
// interruption and must not hide the rest of the run.
func TestListRunsToleratesAHalfWrittenLine(t *testing.T) {
	dir := t.TempDir()
	writeTrajectoryFile(t, dir, "20261018T070400Z-r1.jsonl", finishedRun+`{"v":1,"type":"run.`)
	runs, err := ListRuns(dir, 0)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || !runs[0].Complete {
		t.Fatalf("a half-written trailing line hid the run: %+v", runs)
	}
}

// TestListRunsOnAMissingDirectoryIsEmpty: nothing has run yet, which is not an
// error a caller has to handle.
func TestListRunsOnAMissingDirectoryIsEmpty(t *testing.T) {
	runs, err := ListRuns(filepath.Join(t.TempDir(), "nope"), 0)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("want no runs, got %d", len(runs))
	}
}

// TestValidRunIDIsAPathTraversalGuard: the id becomes a file name, and the
// store's whole value is that it holds what this process wrote.
func TestValidRunIDIsAPathTraversalGuard(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"20261018T070400Z-r1", true},
		{"20261018T070400Z-r1.explore-1", true},
		{"", false},
		{"../../etc/passwd", false},
		{"..", false},
		{"a/b", false},
		{`a\b`, false},
		{"with space", false},
	}
	for _, tc := range cases {
		if got := ValidRunID(tc.id); got != tc.want {
			t.Errorf("ValidRunID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// TestAReadRunCarriesTheUnknownCostCount keeps the accounting vocabulary intact
// through the reader: an unpriced call is reported, not rounded to zero.
func TestAReadRunCarriesTheUnknownCostCount(t *testing.T) {
	dir := t.TempDir()
	body := `{"v":1,"type":"run.start","run":"20261018T070400Z-r1","at":"2026-10-18T07:04:00Z"}
{"v":1,"type":"run.end","run":"20261018T070400Z-r1","stop":"budget","steps":1,"duration_ms":10,"cost_unknown_calls":2,"at":"2026-10-18T07:04:01Z"}
`
	writeTrajectoryFile(t, dir, "20261018T070400Z-r1.jsonl", body)
	runs, err := ListRuns(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].CostUnknown != 2 {
		t.Fatalf("cost_unknown_calls = %d, want 2", runs[0].CostUnknown)
	}
	if len(runs[0].Cost) != 0 {
		t.Fatalf("cost = %v, want none", runs[0].Cost)
	}
	if runs[0].Stop != "budget" {
		t.Fatalf("stop = %q", runs[0].Stop)
	}
	var zero types.Usage
	if runs[0].Tokens != zero {
		t.Fatalf("tokens = %+v, want zero", runs[0].Tokens)
	}
}

// TestAReadRunCountsItsToolCalls is the aggregation the evaluation harness asked
// for: which tool a run leaned on, read from the file rather than by loading it
// into a second parser.
func TestAReadRunCountsItsToolCalls(t *testing.T) {
	dir := t.TempDir()
	body := `{"v":1,"type":"run.start","run":"20261018T070400Z-r1","at":"2026-10-18T07:04:00Z"}
{"v":1,"type":"tool.call","run":"20261018T070400Z-r1","step":0,"call":0,"tool":"read_file","status":"ok","duration_ms":1,"at":"2026-10-18T07:04:00Z"}
{"v":1,"type":"tool.call","run":"20261018T070400Z-r1","step":0,"call":1,"tool":"read_file","status":"ok","duration_ms":1,"at":"2026-10-18T07:04:00Z"}
{"v":1,"type":"tool.call","run":"20261018T070400Z-r1","step":0,"call":2,"tool":"bash","status":"error","duration_ms":1,"at":"2026-10-18T07:04:00Z"}
{"v":1,"type":"run.end","run":"20261018T070400Z-r1","stop":"final","steps":1,"duration_ms":9,"at":"2026-10-18T07:04:01Z"}
`
	writeTrajectoryFile(t, dir, "20261018T070400Z-r1.jsonl", body)
	runs, err := ListRuns(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Tools["read_file"] != 2 || runs[0].Tools["bash"] != 1 {
		t.Fatalf("tool counts = %v, want read_file=2 bash=1", runs[0].Tools)
	}
}

const goalRun = `{"v":1,"type":"run.start","run":"20261019T090000Z-g1","session":"S-3","agent":"build","goal":{"goal_id":"goal-9","round":2,"source":"driver"},"capture":"metadata","at":"2026-10-19T09:00:00Z"}
{"v":1,"type":"run.end","run":"20261019T090000Z-g1","stop":"final","steps":1,"duration_ms":10,"at":"2026-10-19T09:00:01Z"}
`

// TestListRunsReadsTheGoalAttribution pins the goal object on the start line: a
// reader that cannot see it cannot tell a driver round from a human turn.
func TestListRunsReadsTheGoalAttribution(t *testing.T) {
	dir := t.TempDir()
	writeTrajectoryFile(t, dir, "20261019T090000Z-g1.jsonl", goalRun)

	runs, err := ListRuns(dir, 0)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	goal := runs[0].Goal
	if goal == nil {
		t.Fatal("the run's goal attribution was dropped")
	}
	if goal.GoalID != "goal-9" || goal.Round != 2 || goal.Source != types.GoalSourceDriver {
		t.Errorf("goal = %+v, want goal-9 round 2 driver", goal)
	}
}
