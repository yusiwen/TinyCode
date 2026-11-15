package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// toolEvent builds the event shape the recorder writes, with or without captured
// text: the report has to work from what the record holds.
func toolEvent(tool, status, argsDigest, argsText string) map[string]any {
	args := map[string]any{"sha256": argsDigest, "chars": len(argsText)}
	if argsText != "" {
		args["text"] = argsText
	}
	return map[string]any{
		"type":   "tool.call",
		"tool":   tool,
		"status": status,
		"args":   args,
	}
}

func runEndEvent(steps int) map[string]any {
	return map[string]any{"type": "run.end", "steps": float64(steps), "stop": "final"}
}

// TestMeasureProcessReadsWhatTheRecordHolds pins the dimensions against one
// hand-written trajectory: read, write, test, and the order they happened in.
func TestMeasureProcessReadsWhatTheRecordHolds(t *testing.T) {
	events := []map[string]any{
		{"type": "run.start"},
		toolEvent("read_file", "ok", "read1", `{"path":"main.go"}`),
		toolEvent("write_file", "ok", "write1", `{"path":"main.go","content":"x"}`),
		toolEvent("bash", "ok", "bash1", `{"command":"go test ./..."}`),
		runEndEvent(3),
	}
	p := measureProcess(events)
	if p.Steps != 3 {
		t.Errorf("steps = %d, want 3", p.Steps)
	}
	if p.ToolCalls != 3 || p.Reads != 1 || p.Writes != 1 {
		t.Errorf("calls/reads/writes = %d/%d/%d", p.ToolCalls, p.Reads, p.Writes)
	}
	if p.Files != 1 {
		t.Errorf("distinct files written = %d, want 1", p.Files)
	}
	if !p.ReadBeforeWrite {
		t.Error("a read before the write was not counted as evidence gathering")
	}
	if p.Tests != 1 || !p.Verified {
		t.Errorf("tests = %d, verified = %v, want 1/true", p.Tests, p.Verified)
	}
}

// TestVerifiedNeedsATestAfterTheWrite is the dimension that separates checking
// from guessing: a test that ran before the change proves nothing about it.
func TestVerifiedNeedsATestAfterTheWrite(t *testing.T) {
	events := []map[string]any{
		toolEvent("bash", "ok", "b1", `{"command":"go test ./..."}`),
		toolEvent("write_file", "ok", "w1", `{"path":"a.go"}`),
		runEndEvent(2),
	}
	if p := measureProcess(events); p.Verified {
		t.Error("a test that ran before the write counted as verification")
	}

	events = append(events, toolEvent("bash", "ok", "b2", `{"command":"go test ./..."}`))
	if p := measureProcess(events); !p.Verified {
		t.Error("a test after the write did not count as verification")
	}
}

// TestRepeatedAndFailingCalls covers the two signals that a run is stuck: an
// identical call twice, and a call that came back as an error or a refusal.
func TestRepeatedAndFailingCalls(t *testing.T) {
	events := []map[string]any{
		toolEvent("bash", "error", "same", `{"command":"go build ./..."}`),
		toolEvent("bash", "error", "same", `{"command":"go build ./..."}`),
		toolEvent("bash", "refused", "other", `{"command":"rm -rf /"}`),
	}
	p := measureProcess(events)
	if p.Repeated != 1 {
		t.Errorf("repeated = %d, want 1 (the second identical call)", p.Repeated)
	}
	if p.ToolErrors != 3 {
		t.Errorf("tool errors = %d, want 3", p.ToolErrors)
	}
	if p.Refusals != 1 {
		t.Errorf("refusals = %d, want 1", p.Refusals)
	}
}

// TestAMetadataRecordStillYieldsTheCounts is the honest half of full capture: a
// record without argument text can still say how much happened and in what order,
// and it must not invent the dimensions that need the text.
func TestAMetadataRecordStillYieldsTheCounts(t *testing.T) {
	events := []map[string]any{
		toolEvent("read_file", "ok", "r", ""),
		toolEvent("write_file", "ok", "w", ""),
		toolEvent("bash", "ok", "b", ""),
		runEndEvent(3),
	}
	p := measureProcess(events)
	if p.Reads != 1 || p.Writes != 1 || p.ToolCalls != 3 {
		t.Errorf("counts = %+v", p)
	}
	if !p.ReadBeforeWrite {
		t.Error("the read/write order does not need argument text")
	}
	if p.Tests != 0 || p.Verified {
		t.Errorf("verification was invented from a record with no text: %+v", p)
	}
	if p.Files != 0 {
		t.Errorf("distinct files were invented from digests: %d", p.Files)
	}
}

// TestSummarizeProcessAveragesAnAttemptAndItsShare pins the report arithmetic on
// a corpus whose answer is known by hand.
func TestSummarizeProcessAveragesAnAttemptAndItsShare(t *testing.T) {
	mk := func(pass, verified, readFirst, repeated, calls int) runResult {
		return runResult{Pass: pass == 1, Process: &process{
			Steps: calls, ToolCalls: calls, Repeated: repeated,
			ReadBeforeWrite: readFirst == 1, Verified: verified == 1,
		}}
	}
	results := []runResult{
		mk(1, 1, 1, 0, 4),
		mk(1, 1, 1, 0, 6),
		mk(0, 0, 1, 2, 8),
		mk(0, 0, 0, 0, 2),
	}
	s := summarizeProcess("build", results)
	if s.Runs != 4 || s.Steps != 5 || s.Calls != 5 {
		t.Errorf("summary = %+v", s)
	}
	if s.Verified != 50 || s.ReadFirst != 75 {
		t.Errorf("shares = verified %.0f, read-first %.0f; want 50 and 75", s.Verified, s.ReadFirst)
	}
	if s.Repeated != 10 {
		t.Errorf("repeat rate = %.1f%%, want 10%% (2 repeats over 20 calls)", s.Repeated)
	}

	// The association is the question the entry says decides whether a judge is
	// worth building: with verification, 2/2 passed; without, 0/2.
	var verified, notVerified association
	for _, a := range associateProcess(results) {
		switch a.Property {
		case "verified after the last write":
			verified = a
		case "read before writing":
			notVerified = a
		}
	}
	if verified.Present != 2 || verified.PresentPass != 100 || verified.AbsentPass != 0 {
		t.Errorf("verification association = %+v", verified)
	}
	if notVerified.Present != 3 || notVerified.Absent != 1 {
		t.Errorf("read-before-write association = %+v", notVerified)
	}
}

// TestRenderProcessNamesTheSampleSize keeps the caveat attached to the numbers: a
// percentage over three attempts must not read like a measurement.
func TestRenderProcessNamesTheSampleSize(t *testing.T) {
	results := []runResult{{Pass: true, Process: &process{ToolCalls: 1, Verified: true}}}
	out := renderProcess(results, []string{"build"})
	for _, want := range []string{"The process", "build", "Does the process predict the outcome", "not a measurement"} {
		if !strings.Contains(out, want) {
			t.Errorf("the process section does not mention %q:\n%s", want, out)
		}
	}
}

// TestWriteAttemptHomeAsksForFullCapture pins the record setting the process
// report depends on, and the .env copy that keeps a redirected HOME able to
// authenticate.
func TestWriteAttemptHomeAsksForFullCapture(t *testing.T) {
	devHome := t.TempDir()
	t.Setenv("HOME", devHome)
	if err := os.MkdirAll(filepath.Join(devHome, ".tinycode"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(devHome, ".tinycode", ".env"), []byte("DEEPSEEK_API_KEY=k\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	records := t.TempDir()
	if err := writeAttemptHome(home, records); err != nil {
		t.Fatalf("writeAttemptHome: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, ".tinycode", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Trajectory struct {
			Dir     string `json:"dir"`
			Capture string `json:"capture"`
		} `json:"trajectory"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("the attempt home config is not JSON: %v", err)
	}
	if document.Trajectory.Capture != "full" {
		t.Errorf("capture = %q, want full: the process report needs argument text", document.Trajectory.Capture)
	}
	if document.Trajectory.Dir != records {
		t.Errorf("record dir = %q, want %q", document.Trajectory.Dir, records)
	}
	if _, err := os.Stat(filepath.Join(home, ".tinycode", ".env")); err != nil {
		t.Errorf("the key file was not carried into the attempt home: %v", err)
	}
}
