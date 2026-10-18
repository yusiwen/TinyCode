package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
)

// captureStdoutWithError is captureStdout (main_test.go) with the command's own
// error returned: a surface that fails must not look like one that printed
// nothing.
func captureStdoutWithError(t *testing.T, f func() error) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stdout
	os.Stdout = write
	runErr := f()
	os.Stdout = previous
	if closeErr := write.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	output, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("the command returned %v; output was:\n%s", runErr, output)
	}
	return string(output)
}

// TestTheExampleConfigDeclaresTheRecordingDefaults keeps the shipped example and
// the code's defaults from drifting apart, and ties the config layer's numbers to
// the agent package's fallbacks: a store bounded at 200 runs by the example and
// at 64 MiB by the code would bound neither.
func TestTheExampleConfigDeclaresTheRecordingDefaults(t *testing.T) {
	// Through the real loader, not a raw decode: the example carries $comment
	// notes that only the loader strips, and the loader is what a user's run
	// actually uses.
	example := loadShippedExampleConfig(t)
	spec := example.TrajectorySpec()
	if !spec.Enabled {
		t.Fatal("the example switches recording off")
	}
	if spec.Capture != config.TrajectoryCaptureMetadata {
		t.Fatalf("the example's capture = %q, want metadata", spec.Capture)
	}
	if spec.MaxRuns != agent.TrajectoryMaxRuns || spec.MaxBytes != agent.TrajectoryMaxBytes {
		t.Fatalf("the example bounds are %d / %d, the agent's fallbacks are %d / %d",
			spec.MaxRuns, spec.MaxBytes, agent.TrajectoryMaxRuns, agent.TrajectoryMaxBytes)
	}
	if spec.MaxRuns != config.DefaultTrajectoryMaxRuns || spec.MaxBytes != config.DefaultTrajectoryMaxBytes {
		t.Fatalf("the example bounds differ from the config defaults")
	}
}

// TestTrajectoriesListsWhatTheStoreHolds drives the list command against files
// written by hand, so it exercises the reader and the printer rather than the
// writer they ship with.
func TestTrajectoriesListsWhatTheStoreHolds(t *testing.T) {
	dir := t.TempDir()
	complete := `{"v":1,"type":"run.start","run":"20261018T070400Z-r1","session":"S-1","agent":"build","at":"2026-10-18T07:04:00Z"}
{"v":1,"type":"run.end","run":"20261018T070400Z-r1","stop":"final","steps":3,"duration_ms":4200,"tokens":{"prompt_tokens":1000,"completion_tokens":288,"total_tokens":1288},"cost":[{"unit":"USD","amount":0.000231}],"at":"2026-10-18T07:04:04Z"}
`
	if err := os.WriteFile(filepath.Join(dir, "20261018T070400Z-r1.jsonl"), []byte(complete), 0o600); err != nil {
		t.Fatal(err)
	}
	interrupted := `{"v":1,"type":"run.start","run":"20261018T080000Z-r2","at":"2026-10-18T08:00:00Z"}
{"v":1,"type":"llm.call","run":"20261018T080000Z-r2","step":0,"duration_ms":5,"outcome":"final","at":"2026-10-18T08:00:00Z"}
`
	if err := os.WriteFile(filepath.Join(dir, "20261018T080000Z-r2.jsonl"), []byte(interrupted), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{Trajectory: &config.TrajectoryConfig{Dir: dir}}
	output := captureStdoutWithError(t, func() error { return printTrajectories(&cfg, 20) })

	for _, want := range []string{
		"20261018T070400Z-r1", "final", "3 steps", "1288 tok", "0.000231",
		"20261018T080000Z-r2", "incomplete",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("the listing is missing %q:\n%s", want, output)
		}
	}
	// Newest first: the interrupted run started later, so it is printed first.
	if strings.Index(output, "20261018T080000Z-r2") > strings.Index(output, "20261018T070400Z-r1") {
		t.Errorf("the listing is not newest-first:\n%s", output)
	}
}

// TestTrajectoriesOnAnEmptyStoreSaysSo: a directory that does not exist yet is a
// fact to report, not an error to hand back.
func TestTrajectoriesOnAnEmptyStoreSaysSo(t *testing.T) {
	cfg := config.Config{Trajectory: &config.TrajectoryConfig{Dir: filepath.Join(t.TempDir(), "none")}}
	output := captureStdoutWithError(t, func() error { return printTrajectories(&cfg, 20) })
	if !strings.Contains(output, "No trajectories") {
		t.Fatalf("output = %q", output)
	}
}

// TestTrajectoryPrintsOneFileVerbatim: the export is the file, so the bytes have
// to be the same bytes an analysis script would read.
func TestTrajectoryPrintsOneFileVerbatim(t *testing.T) {
	dir := t.TempDir()
	body := `{"v":1,"type":"run.start","run":"20261018T070400Z-r1","at":"2026-10-18T07:04:00Z"}
`
	if err := os.WriteFile(filepath.Join(dir, "20261018T070400Z-r1.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Trajectory: &config.TrajectoryConfig{Dir: dir}}
	if got := captureStdoutWithError(t, func() error { return printTrajectory(&cfg, "20261018T070400Z-r1") }); got != body {
		t.Fatalf("printed %q, want the file verbatim", got)
	}
}

// TestTrajectoryRefusesAnIdThatIsAPath pins the traversal guard at the command,
// where a user's typo or a pasted path arrives.
func TestTrajectoryRefusesAnIdThatIsAPath(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{Trajectory: &config.TrajectoryConfig{Dir: dir}}

	err := printTrajectory(&cfg, "../../etc/passwd")
	if err == nil {
		t.Fatal("a path was accepted as a run id")
	}
	// The refusal has to be the guard's, not "no such file": without the guard
	// the traversal is still attempted, so an error alone proves nothing — the
	// store is only worth something if it cannot be asked for a file it never
	// wrote, whether or not that file exists today.
	if !strings.Contains(err.Error(), "is not a run id") {
		t.Fatalf("error = %v, want the id validation's refusal", err)
	}
}
