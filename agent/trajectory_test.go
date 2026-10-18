package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// seqClock hands out one instant per read, one second apart, so a test can
// assert an exact duration instead of "greater than zero": a recording that
// measured with the wall clock would produce a number this clock never gave.
type seqClock struct {
	base time.Time
	mu   sync.Mutex
	read int
}

func newSeqClock() *seqClock {
	return &seqClock{base: time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)}
}

func (c *seqClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.read++
	return c.base.Add(time.Duration(c.read) * time.Second)
}

func (c *seqClock) Reads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.read
}

// trajProvider is a scripted provider that also streams, so the first-delta
// measurement can be driven. MockLLM reports neither usage nor deltas.
type trajProvider struct {
	mu        sync.Mutex
	responses []*types.ChatResponse
	deltas    []string
	calls     int
}

func (p *trajProvider) Name() string         { return "trajectory-test" }
func (p *trajProvider) Route() string        { return "testroute" }
func (p *trajProvider) DefaultModel() string { return "testmodel" }
func (p *trajProvider) CostCurrency() string { return "USD" }

func (p *trajProvider) Chat(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := p.calls
	p.calls++
	if req.StreamCallbacks != nil {
		for _, delta := range p.deltas {
			if req.StreamCallbacks.OnTextDelta != nil {
				req.StreamCallbacks.OnTextDelta(delta)
			}
		}
	}
	if index < len(p.responses) {
		return p.responses[index], nil
	}
	return &types.ChatResponse{Content: "done"}, nil
}

func (p *trajProvider) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// trajTool is a tool whose result and duration the test controls.
func trajTool(name, result string, delay time.Duration) Tool {
	return Tool{
		Name:        name,
		Description: "test tool",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		Execute: func(context.Context, map[string]any) (string, error) {
			time.Sleep(delay)
			return result, nil
		},
	}
}

// trajectoryFiles lists the JSONL files in dir, sorted by name.
func trajectoryFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			names = append(names, entry.Name())
		}
	}
	return names
}

// trajectoryLines parses one file into per-line objects.
func trajectoryLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var events []map[string]any
	for i, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("%s line %d is not JSON: %v\n%s", path, i+1, err, line)
		}
		events = append(events, event)
	}
	return events
}

// onlyRun returns the single file in dir and its events.
func onlyRun(t *testing.T, dir string) (string, []map[string]any) {
	t.Helper()
	files := trajectoryFiles(t, dir)
	if len(files) != 1 {
		t.Fatalf("want exactly one trajectory file, got %d: %v", len(files), files)
	}
	path := filepath.Join(dir, files[0])
	return path, trajectoryLines(t, path)
}

// ofType returns the events of one kind.
func ofType(events []map[string]any, kind string) []map[string]any {
	var out []map[string]any
	for _, event := range events {
		if event["type"] == kind {
			out = append(out, event)
		}
	}
	return out
}

func number(t *testing.T, event map[string]any, field string) float64 {
	t.Helper()
	value, ok := event[field].(float64)
	if !ok {
		t.Fatalf("field %q is %v, want a number", field, event[field])
	}
	return value
}

// TestARunLeavesOneParsableFilePerRun is the basic shape: a file whose first
// line opens the run and whose last line closes it, with every line carrying the
// same run id as the file name.
func TestARunLeavesOneParsableFilePerRun(t *testing.T) {
	dir := t.TempDir()
	a := New(&trajProvider{responses: []*types.ChatResponse{{Content: "hello"}}})
	a.Trajectory = NewTrajectory(dir, TrajectoryCaptureMetadata)
	a.SessionID = "S-1"
	a.Now = newSeqClock().Now

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	path, events := onlyRun(t, dir)
	if len(events) < 4 {
		t.Fatalf("want at least run.start, llm.call, step.end, run.end; got %d lines", len(events))
	}
	if events[0]["type"] != TrajectoryRunStart {
		t.Fatalf("first line is %v, want %s", events[0]["type"], TrajectoryRunStart)
	}
	if last := events[len(events)-1]; last["type"] != TrajectoryRunEnd {
		t.Fatalf("last line is %v, want %s", last["type"], TrajectoryRunEnd)
	}

	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	for _, event := range events {
		if event["run"] != id {
			t.Fatalf("a line carries run %v, want %q", event["run"], id)
		}
		if event["v"] != float64(TrajectoryVersion) {
			t.Fatalf("a line carries version %v, want %d", event["v"], TrajectoryVersion)
		}
		if event["session"] != "S-1" {
			t.Fatalf("a line carries session %v, want S-1", event["session"])
		}
		if _, err := time.Parse(time.RFC3339Nano, event["at"].(string)); err != nil {
			t.Fatalf("at is not RFC3339: %v", event["at"])
		}
	}

	start := events[0]
	if start["provider"] != "testroute" || start["model"] != "testmodel" {
		t.Fatalf("run.start names %v/%v, want testroute/testmodel", start["provider"], start["model"])
	}
	if start["capture"] != TrajectoryCaptureMetadata {
		t.Fatalf("run.start capture = %v", start["capture"])
	}
}

// TestDurationsComeFromTheInjectedClock is the anti-wall-clock test. The clock
// advances one second per read, and the loop's read order is fixed for a run
// with no tool calls, so every duration has exactly one correct value.
func TestDurationsComeFromTheInjectedClock(t *testing.T) {
	dir := t.TempDir()
	clock := newSeqClock()

	a := New(&trajProvider{responses: []*types.ChatResponse{{Content: "hello"}}})
	a.Trajectory = NewTrajectory(dir, TrajectoryCaptureMetadata)
	a.Now = clock.Now

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, events := onlyRun(t, dir)

	// Reads: 1 run start, 2 step start, 3 call start, 4 call end, 5 step end,
	// 6 run end.
	calls := ofType(events, TrajectoryLLMCall)
	if len(calls) != 1 {
		t.Fatalf("want one llm.call, got %d", len(calls))
	}
	if got := number(t, calls[0], "duration_ms"); got != 1000 {
		t.Fatalf("llm.call duration_ms = %v, want 1000 (reads 3 and 4)", got)
	}
	steps := ofType(events, TrajectoryStepEnd)
	if len(steps) != 1 {
		t.Fatalf("want one step.end, got %d", len(steps))
	}
	if got := number(t, steps[0], "duration_ms"); got != 3000 {
		t.Fatalf("step.end duration_ms = %v, want 3000 (reads 2 and 5)", got)
	}
	end := ofType(events, TrajectoryRunEnd)[0]
	if got := number(t, end, "duration_ms"); got != 5000 {
		t.Fatalf("run.end duration_ms = %v, want 5000 (reads 1 and 6)", got)
	}
	if clock.Reads() != 6 {
		t.Fatalf("the clock was read %d times, want 6: a recorder reading it more often would break the pricing invariant", clock.Reads())
	}
}

// TestToolCallsAreWrittenInCallOrder pins the ordering rule: the calls run
// concurrently, and the first one is deliberately the slowest, so completion
// order is the reverse of call order.
func TestToolCallsAreWrittenInCallOrder(t *testing.T) {
	dir := t.TempDir()
	provider := &trajProvider{responses: []*types.ChatResponse{
		{ToolCalls: []types.ToolCall{
			{ID: "1", Name: "slow", Arguments: `{}`},
			{ID: "2", Name: "middle", Arguments: `{}`},
			{ID: "3", Name: "fast", Arguments: `{}`},
		}},
		{Content: "done"},
	}}

	a := New(provider)
	a.Trajectory = NewTrajectory(dir, TrajectoryCaptureMetadata)
	a.Now = newSeqClock().Now
	a.AddTool(trajTool("slow", "slow-result", 30*time.Millisecond))
	a.AddTool(trajTool("middle", "middle-result", 10*time.Millisecond))
	a.AddTool(trajTool("fast", "fast-result", 0))

	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	_, events := onlyRun(t, dir)

	calls := ofType(events, TrajectoryToolCall)
	if len(calls) != 3 {
		t.Fatalf("want three tool.call lines, got %d", len(calls))
	}
	for index, want := range []string{"slow", "middle", "fast"} {
		if calls[index]["tool"] != want {
			t.Fatalf("tool.call %d is %v, want %s", index, calls[index]["tool"], want)
		}
		if got := number(t, calls[index], "call"); got != float64(index) {
			t.Fatalf("tool %s carries call %v, want %d", want, got, index)
		}
		if calls[index]["status"] != "ok" {
			t.Fatalf("tool %s status = %v, want ok", want, calls[index]["status"])
		}
	}
}

// TestMetadataCaptureOmitsTextAndKeepsTheDigest is the privacy property, with the
// control that makes it meaningful: the same run in both modes must agree on the
// digest. Otherwise "metadata" could be hiding a different digest too.
func TestMetadataCaptureOmitsTextAndKeepsTheDigest(t *testing.T) {
	const argument = `{"path":"/tmp/secret.txt"}`
	const result = "the file contents, which may hold a secret"

	run := func(t *testing.T, capture string) (raw string, events []map[string]any) {
		t.Helper()
		dir := t.TempDir()
		provider := &trajProvider{responses: []*types.ChatResponse{
			{ToolCalls: []types.ToolCall{{ID: "1", Name: "read", Arguments: argument}}},
			{Content: "done"},
		}}
		a := New(provider)
		a.Trajectory = NewTrajectory(dir, capture)
		a.Now = newSeqClock().Now
		a.AddTool(Tool{
			Name:        "read",
			Description: "reads",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path": map[string]any{"type": "string"},
				},
			},
			Execute: func(context.Context, map[string]any) (string, error) { return result, nil },
		})
		if _, err := a.Run(context.Background(), "read it"); err != nil {
			t.Fatalf("Run: %v", err)
		}
		path, lines := onlyRun(t, dir)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data), lines
	}

	metadataRaw, metadataEvents := run(t, TrajectoryCaptureMetadata)
	if strings.Contains(metadataRaw, argument) || strings.Contains(metadataRaw, result) {
		t.Fatalf("metadata capture wrote the text to disk:\n%s", metadataRaw)
	}
	if !strings.Contains(metadataRaw, `"sha256"`) {
		t.Fatalf("metadata capture wrote no digest:\n%s", metadataRaw)
	}

	sum := sha256.Sum256([]byte(result))
	wantDigest := hex.EncodeToString(sum[:])
	tool := ofType(metadataEvents, TrajectoryToolCall)[0]
	resultObject := tool["result"].(map[string]any)
	if resultObject["sha256"] != wantDigest {
		t.Fatalf("result sha256 = %v, want %s", resultObject["sha256"], wantDigest)
	}
	if resultObject["chars"] != float64(len(result)) {
		t.Fatalf("result chars = %v, want %d", resultObject["chars"], len(result))
	}
	if _, ok := resultObject["text"]; ok {
		t.Fatal("metadata capture carries a text field")
	}

	fullRaw, fullEvents := run(t, TrajectoryCaptureFull)
	if !strings.Contains(fullRaw, result) {
		t.Fatalf("full capture did not write the text:\n%s", fullRaw)
	}
	fullTool := ofType(fullEvents, TrajectoryToolCall)[0]
	fullResult := fullTool["result"].(map[string]any)
	if fullResult["sha256"] != wantDigest {
		t.Fatalf("full capture sha256 = %v, want %s: the digest must not depend on the capture mode", fullResult["sha256"], wantDigest)
	}
	if fullResult["text"] != result {
		t.Fatalf("full capture text = %v", fullResult["text"])
	}
}

// TestTheRunEndNamesTheStop is the vocabulary F1 groups by: a budget stop must
// not be recorded as a finished run.
func TestTheRunEndNamesTheStop(t *testing.T) {
	t.Run("final answer", func(t *testing.T) {
		dir := t.TempDir()
		a := New(&trajProvider{responses: []*types.ChatResponse{{Content: "done"}}})
		a.Trajectory = NewTrajectory(dir, TrajectoryCaptureMetadata)
		a.Now = newSeqClock().Now
		if _, err := a.Run(context.Background(), "hi"); err != nil {
			t.Fatal(err)
		}
		_, events := onlyRun(t, dir)
		end := ofType(events, TrajectoryRunEnd)[0]
		if end["stop"] != trajectoryStopFinal {
			t.Fatalf("stop = %v, want %s", end["stop"], trajectoryStopFinal)
		}
		if got := number(t, end, "steps"); got != 1 {
			t.Fatalf("steps = %v, want 1", got)
		}
	})

	t.Run("budget", func(t *testing.T) {
		dir := t.TempDir()
		usage := types.Usage{PromptTokens: 50, CompletionTokens: 50, TotalTokens: 100}
		provider := &trajProvider{responses: []*types.ChatResponse{
			{ToolCalls: []types.ToolCall{{ID: "1", Name: "noop", Arguments: `{}`}}, Usage: &usage},
			{Content: "never reached", Usage: &usage},
		}}
		a := New(provider)
		a.Trajectory = NewTrajectory(dir, TrajectoryCaptureMetadata)
		a.Now = newSeqClock().Now
		a.BudgetTokensPerRun = 1
		a.AddTool(trajTool("noop", "ok", 0))

		if _, err := a.Run(context.Background(), "go"); err != nil {
			t.Fatal(err)
		}
		if provider.CallCount() != 1 {
			t.Fatalf("provider was called %d times, want 1", provider.CallCount())
		}
		_, events := onlyRun(t, dir)
		end := ofType(events, TrajectoryRunEnd)[0]
		if end["stop"] != trajectoryStopBudget {
			t.Fatalf("stop = %v, want %s", end["stop"], trajectoryStopBudget)
		}
		if got := number(t, end, "steps"); got != 1 {
			t.Fatalf("steps = %v, want 1: a step that made a call counts", got)
		}
	})
}

// TestAChildRunIsNumberedByItsParent pins the delegation naming: two children of
// one parent start in the same second, so an id each derived itself would
// collide in the store.
func TestAChildRunIsNumberedByItsParent(t *testing.T) {
	dir := t.TempDir()
	trajectory := NewTrajectory(dir, TrajectoryCaptureMetadata)
	parent := trajectory.Start(RunMetadata{Agent: "build"})
	if parent == nil {
		t.Fatal("Start returned nil")
	}
	first := parent.Child("explore")
	second := parent.Child("explore")
	if first == nil || second == nil {
		t.Fatal("Child returned nil")
	}
	if first.ID() == second.ID() {
		t.Fatalf("two children share the id %q", first.ID())
	}
	if !strings.HasPrefix(first.ID(), parent.ID()) {
		t.Fatalf("child id %q does not name its parent %q", first.ID(), parent.ID())
	}
	first.End(trajectoryStopFinal, 1, time.Second, types.Usage{}, nil, 0)
	second.End(trajectoryStopFinal, 1, time.Second, types.Usage{}, nil, 0)
	parent.End(trajectoryStopFinal, 1, time.Second, types.Usage{}, nil, 0)

	if files := trajectoryFiles(t, dir); len(files) != 3 {
		t.Fatalf("want the parent and two children on disk, got %d: %v", len(files), files)
	}
	childPath := filepath.Join(dir, first.ID()+".jsonl")
	childEvents := trajectoryLines(t, childPath)
	start := childEvents[0]
	if start["parent"] != parent.ID() {
		t.Fatalf("child parent = %v, want %s", start["parent"], parent.ID())
	}
	if start["agent"] != "explore" {
		t.Fatalf("child agent = %v, want explore", start["agent"])
	}
}

// TestARecordingFailureDoesNotFailTheRun is the opposite of the checkpoint rule,
// on purpose: recording is observability, so a broken store costs a warning and
// nothing else.
func TestARecordingFailureDoesNotFailTheRun(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}

	a := New(&trajProvider{responses: []*types.ChatResponse{{Content: "still works"}}})
	a.Trajectory = NewTrajectory(blocked, TrajectoryCaptureMetadata)
	a.Now = newSeqClock().Now

	answer, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Run failed because a trajectory could not be written: %v", err)
	}
	if answer != "still works" {
		t.Fatalf("answer = %q", answer)
	}
	if a.trajRun != nil {
		t.Fatal("a failed recording left a run handle behind")
	}
}

// TestNoTrajectoryWritesNothing pins "off": nothing is created, and the clock is
// not read for a recording that does not exist.
func TestNoTrajectoryWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trajectories")
	a := New(&trajProvider{responses: []*types.ChatResponse{{Content: "done"}}})
	clock := newSeqClock()
	a.Now = clock.Now

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("a directory was created with no trajectory configured: %v", err)
	}
	// One read remains, and it has to: it is the instant the call is priced at,
	// which exists whether or not anything is being recorded. A second would be
	// the recorder's.
	if clock.Reads() != 1 {
		t.Fatalf("the clock was read %d times with no trajectory, want 1 (the pricing instant)", clock.Reads())
	}
}

// TestAnUnknownCostIsRecordedAsUnknown keeps the accounting rule the rest of the
// codebase uses: a call nobody priced is unknown, which is not zero.
func TestAnUnknownCostIsRecordedAsUnknown(t *testing.T) {
	dir := t.TempDir()
	usage := types.Usage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20}
	a := New(&trajProvider{responses: []*types.ChatResponse{{Content: "done", Usage: &usage}}})
	a.Trajectory = NewTrajectory(dir, TrajectoryCaptureMetadata)
	a.Now = newSeqClock().Now

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	_, events := onlyRun(t, dir)

	call := ofType(events, TrajectoryLLMCall)[0]
	cost, ok := call["cost"].(map[string]any)
	if !ok {
		t.Fatalf("llm.call carries no cost object: %v", call)
	}
	if cost["source"] != string(types.CostUnknown) {
		t.Fatalf("cost source = %v, want %s", cost["source"], types.CostUnknown)
	}
	if tokens, ok := call["tokens"].(map[string]any); !ok || tokens["total_tokens"] != float64(20) {
		t.Fatalf("llm.call tokens = %v, want the reported 20", call["tokens"])
	}
	end := ofType(events, TrajectoryRunEnd)[0]
	if got := number(t, end, "cost_unknown_calls"); got != 1 {
		t.Fatalf("cost_unknown_calls = %v, want 1", got)
	}
}

// TestToolStatusVocabulary pins the classification of a tool result, which is
// what an evaluation harness groups failures by.
func TestToolStatusVocabulary(t *testing.T) {
	cases := []struct {
		name   string
		result string
		want   string
	}{
		{"ordinary output", "file contents", "ok"},
		{"empty output", "", "ok"},
		{"dispatcher error", "error: exit status 1", "error"},
		{"parse failure", "error parsing args: unexpected end of JSON input", "error"},
		{"invalid arguments", "invalid arguments for read_file: path is required", "error"},
		{"unknown tool", "unknown tool: nope", "error"},
		{"denied in mode", "[DENIED] bash is not available in plan mode.", "error"},
		{"repeat breaker", "[LOOP] bash has already been called with exactly these arguments 3 times and failed every time.", "repeated"},
		{"sandbox refusal", "[SANDBOX] path \"/etc/passwd\": is outside the sandbox\nmode: workspace-write\nask: interactive", "refused"},
		{"terminal refusal", "[SANDBOX] path \"/etc/passwd\": is outside the sandbox\nmode: read-only\nask: policy", "blocked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolStatus(tc.result); got != tc.want {
				t.Fatalf("toolStatus(%q) = %q, want %q", tc.result, got, tc.want)
			}
		})
	}
}

// TestTTFTIsMeasuredFromTheFirstDelta measures the one latency figure a reader
// of a trajectory actually needs: how long the silence lasted.
func TestTTFTIsMeasuredFromTheFirstDelta(t *testing.T) {
	dir := t.TempDir()
	provider := &trajProvider{
		responses: []*types.ChatResponse{{Content: "hello"}},
		deltas:    []string{"he", "llo"},
	}
	a := New(provider)
	a.Trajectory = NewTrajectory(dir, TrajectoryCaptureMetadata)
	a.Now = newSeqClock().Now

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	_, events := onlyRun(t, dir)
	call := ofType(events, TrajectoryLLMCall)[0]
	// Reads: 1 run start, 2 step start, 3 call start, 4 first delta. The second
	// delta must not move it.
	if got := number(t, call, "ttft_ms"); got != 1000 {
		t.Fatalf("ttft_ms = %v, want 1000 (reads 3 and 4)", got)
	}
}

// TestPruneKeepsTheNewestAndSparesLiveRuns is the retention bound, and the one
// case that must survive it: a run whose file is still open is not deletable,
// because the file is about to be appended to.
func TestPruneKeepsTheNewestAndSparesLiveRuns(t *testing.T) {
	dir := t.TempDir()
	trajectory := NewTrajectory(dir, TrajectoryCaptureMetadata)
	base := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	// Five finished runs, oldest first.
	for i := 0; i < 5; i++ {
		run := trajectory.Start(RunMetadata{Agent: "build"})
		if run == nil {
			t.Fatal("Start returned nil")
		}
		run.End(trajectoryStopFinal, 1, time.Second, types.Usage{}, nil, 0)
	}
	files := trajectoryFiles(t, dir)
	if len(files) != 5 {
		t.Fatalf("want five files, got %d", len(files))
	}
	for i, name := range files {
		stamp := base.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(filepath.Join(dir, name), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	// One run still in flight, and the newest on disk.
	live := trajectory.Start(RunMetadata{Agent: "build"})
	if live == nil {
		t.Fatal("Start returned nil for the live run")
	}
	future := base.Add(100 * time.Hour)
	if err := os.Chtimes(filepath.Join(dir, live.ID()+".jsonl"), future, future); err != nil {
		t.Fatal(err)
	}

	removed, err := trajectory.Prune(2, TrajectoryMaxBytes)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 3 {
		t.Fatalf("removed %d files, want 3", removed)
	}
	remaining := trajectoryFiles(t, dir)
	if len(remaining) != 3 {
		t.Fatalf("want three files left (two newest plus the live one), got %d: %v", len(remaining), remaining)
	}
	if _, err := os.Stat(filepath.Join(dir, live.ID()+".jsonl")); err != nil {
		t.Fatalf("the live run was pruned: %v", err)
	}
	for _, name := range remaining[:2] {
		if name == files[0] || name == files[1] {
			t.Fatalf("prune kept the oldest file %s", name)
		}
	}
	live.End(trajectoryStopFinal, 1, time.Second, types.Usage{}, nil, 0)
}

// TestPruneAlsoBoundsBytes pins the second bound: a store under the file count
// but over its size still loses its oldest files.
func TestPruneAlsoBoundsBytes(t *testing.T) {
	dir := t.TempDir()
	trajectory := NewTrajectory(dir, TrajectoryCaptureMetadata)
	for i := 0; i < 3; i++ {
		path := filepath.Join(dir, "old-"+string(rune('a'+i))+".jsonl")
		if err := os.WriteFile(path, make([]byte, 1024), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Date(2026, 10, 1, i, 0, 0, 0, time.UTC)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := trajectory.Prune(TrajectoryMaxRuns, 1024)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 2 {
		t.Fatalf("removed %d files, want 2", removed)
	}
	if left := trajectoryFiles(t, dir); len(left) != 1 {
		t.Fatalf("want the newest file left, got %v", left)
	}
}
