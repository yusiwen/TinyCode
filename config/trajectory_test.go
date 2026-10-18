package config

import (
	"path/filepath"
	"testing"
)

// TestTrajectorySpecDefaults pins the defaults: recording is on, capture keeps
// no text, and the store is bounded. Each of those is a decision, not a zero
// value: defaulting recording off would leave an evaluation with nothing to read,
// and defaulting capture to full would write tool arguments to disk without
// being asked.
func TestTrajectorySpecDefaults(t *testing.T) {
	spec := DefaultConfig().TrajectorySpec()
	if !spec.Enabled {
		t.Fatal("recording is off by default")
	}
	if spec.Capture != TrajectoryCaptureMetadata {
		t.Fatalf("capture = %q, want %q", spec.Capture, TrajectoryCaptureMetadata)
	}
	if spec.MaxRuns != DefaultTrajectoryMaxRuns || spec.MaxBytes != DefaultTrajectoryMaxBytes {
		t.Fatalf("bounds = %d runs / %d bytes, want %d / %d",
			spec.MaxRuns, spec.MaxBytes, DefaultTrajectoryMaxRuns, DefaultTrajectoryMaxBytes)
	}
	if filepath.Base(spec.Dir) != "trajectories" || filepath.Base(filepath.Dir(spec.Dir)) != ".tinycode" {
		t.Fatalf("dir = %q, want <home>/.tinycode/trajectories", spec.Dir)
	}
}

// TestTrajectorySpecResolvesEverySetting is the control for the defaults: what a
// file says is what the spec reports.
func TestTrajectorySpecResolvesEverySetting(t *testing.T) {
	off := false
	cfg := Config{Trajectory: &TrajectoryConfig{
		Enabled:  &off,
		Dir:      "/tmp/traj",
		Capture:  "FULL", // case-insensitive, like every other mode word here
		MaxRuns:  7,
		MaxBytes: 1024,
	}}
	spec := cfg.TrajectorySpec()
	if spec.Enabled {
		t.Fatal("enabled: false was not honoured")
	}
	if spec.Dir != "/tmp/traj" {
		t.Fatalf("dir = %q", spec.Dir)
	}
	if spec.Capture != TrajectoryCaptureFull {
		t.Fatalf("capture = %q, want full", spec.Capture)
	}
	if spec.MaxRuns != 7 || spec.MaxBytes != 1024 {
		t.Fatalf("bounds = %d / %d", spec.MaxRuns, spec.MaxBytes)
	}
}

// TestAnUnknownCaptureIsMetadataNotAnError pins the forgiving direction: a typo
// reads as the safer mode, because failing a start over a spelling would be a
// worse trade than recording less.
func TestAnUnknownCaptureIsMetadataNotAnError(t *testing.T) {
	cfg := Config{Trajectory: &TrajectoryConfig{Capture: "everything"}}
	if got := cfg.TrajectorySpec().Capture; got != TrajectoryCaptureMetadata {
		t.Fatalf("capture = %q, want metadata", got)
	}
}

// TestMergeTakesTheTrajectorySectionFieldByField lets a project file narrow the
// store without dropping the user's capture mode, and lets `enabled` be said
// with a pointer so a later layer's absent field cannot switch it back on.
func TestMergeTakesTheTrajectorySectionFieldByField(t *testing.T) {
	off := false
	user := DefaultConfig()
	user.Trajectory = &TrajectoryConfig{Dir: "/from/user", Capture: "full", MaxRuns: 5}

	project := Config{Trajectory: &TrajectoryConfig{MaxBytes: 2048, Enabled: &off}}
	merged := merge(user, project)

	if merged.Trajectory.Dir != "/from/user" {
		t.Fatalf("dir = %q, want the user's", merged.Trajectory.Dir)
	}
	if merged.Trajectory.Capture != "full" {
		t.Fatalf("capture = %q, want the user's full", merged.Trajectory.Capture)
	}
	if merged.Trajectory.MaxRuns != 5 || merged.Trajectory.MaxBytes != 2048 {
		t.Fatalf("bounds = %d / %d, want each layer's own", merged.Trajectory.MaxRuns, merged.Trajectory.MaxBytes)
	}
	if merged.Trajectory.Enabled == nil || *merged.Trajectory.Enabled {
		t.Fatal("the project's enabled: false did not survive the merge")
	}
}

// TestTheUserLayerAloneMayAskForFullCapture is the privacy rule: a cloned
// repository can switch recording off, and cannot switch text capture on.
func TestTheUserLayerAloneMayAskForFullCapture(t *testing.T) {
	projectOnly := Config{Trajectory: &TrajectoryConfig{Capture: "full"}}
	capture, ignored := UserLayer{raw: map[string]any{}}.ResolveTrajectoryCapture(&projectOnly)
	if capture != TrajectoryCaptureMetadata || !ignored {
		t.Fatalf("capture = %q ignored = %v, want metadata and the ignore reported", capture, ignored)
	}

	userAsks := Config{Trajectory: &TrajectoryConfig{Capture: "full"}}
	layer := UserLayer{raw: map[string]any{"trajectory": map[string]any{"capture": "full"}}}
	capture, ignored = layer.ResolveTrajectoryCapture(&userAsks)
	if capture != TrajectoryCaptureFull || ignored {
		t.Fatalf("capture = %q ignored = %v, want full from the user's own file", capture, ignored)
	}

	// The control: metadata never needs the veto, whoever asks for it.
	metadata := Config{Trajectory: &TrajectoryConfig{Capture: "metadata"}}
	if capture, ignored := (UserLayer{raw: map[string]any{}}).ResolveTrajectoryCapture(&metadata); capture != TrajectoryCaptureMetadata || ignored {
		t.Fatalf("metadata was reported as ignored")
	}
}
