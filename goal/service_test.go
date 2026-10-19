package goal

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// testService returns a service with a deterministic clock and ids, so a
// revision or a timestamp is a fact a test can assert.
func testService() *Service {
	s := NewService()
	base := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	ticks, ids := 0, 0
	s.Now = func() time.Time {
		ticks++
		return base.Add(time.Duration(ticks) * time.Second)
	}
	s.NewID = func(time.Time) string {
		ids++
		return fmt.Sprintf("goal-%d", ids)
	}
	return s
}

func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var ge *Error
	if !errors.As(err, &ge) {
		t.Fatalf("error %v is not a goal.Error", err)
	}
	if ge.Code != code {
		t.Fatalf("error code = %q, want %q (%v)", ge.Code, code, err)
	}
}

func snapshot(t *testing.T, s *Service) *Goal {
	t.Helper()
	g := s.Snapshot()
	if g == nil {
		t.Fatal("no goal is current")
	}
	return g
}

func TestCreateArmsAnActiveGoal(t *testing.T) {
	s := testService()
	g, err := s.Create("  make the failing test pass  ", 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if g.Objective != "make the failing test pass" {
		t.Errorf("objective = %q, want the trimmed text", g.Objective)
	}
	if g.Phase != PhaseActive || g.RoundsStarted != 0 || g.Revision != 1 {
		t.Errorf("created goal = %+v, want active, 0 rounds, revision 1", g)
	}
	if g.MaxGoalRounds != DefaultMaxRounds {
		t.Errorf("max rounds = %d, want the default %d", g.MaxGoalRounds, DefaultMaxRounds)
	}
	if !s.Armed() {
		t.Error("a created goal must be armed: a human asked for it")
	}
	if g.CreatedAt.IsZero() || g.UpdatedAt.IsZero() {
		t.Error("a created goal must carry timestamps")
	}
	if err := g.Validate(); err != nil {
		t.Errorf("created goal does not validate: %v", err)
	}

	if _, err := s.Create("  ", 0); err == nil {
		t.Error("an empty objective must be refused")
	} else {
		requireCode(t, err, ErrInvalid)
	}
}

func TestCreateRefusesWhileAnObjectiveIsUnfinished(t *testing.T) {
	s := testService()
	first, err := s.Create("first", 4)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Create("second", 4); err == nil {
		t.Fatal("a second goal must be refused while the first is unfinished")
	} else {
		requireCode(t, err, ErrUnfinished)
	}

	// Finishing it frees the slot; the next goal is a fresh identity.
	if _, err := s.Complete(first.Ref()); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	second, err := s.Create("second", 4)
	if err != nil {
		t.Fatalf("Create after complete: %v", err)
	}
	if second.ID == first.ID {
		t.Errorf("a replacement goal reused the id %q", second.ID)
	}
	if second.RoundsStarted != 0 || second.Revision != 1 {
		t.Errorf("a replacement goal started at %+v, want 0 rounds and revision 1", second)
	}
}

func TestTransitions(t *testing.T) {
	type step struct {
		name      string
		setup     []string // operations applied before the step under test
		run       func(*Service, *Goal) (*Goal, error)
		wantPhase Phase
		wantArm   bool
		wantDelta int // revision change
		errCode   string
	}

	steps := []step{
		{
			name: "edit keeps the phase", wantPhase: PhaseActive, wantArm: true, wantDelta: 1,
			run: func(s *Service, g *Goal) (*Goal, error) { return s.Edit(g.Ref(), "new objective") },
		},
		{
			name: "pause", wantPhase: PhasePaused, wantDelta: 1,
			run: func(s *Service, g *Goal) (*Goal, error) { return s.Pause(g.Ref()) },
		},
		{
			name: "resume from paused", setup: []string{"pause"}, wantPhase: PhaseActive, wantArm: true, wantDelta: 1,
			run: func(s *Service, g *Goal) (*Goal, error) { return s.Resume(g.Ref()) },
		},
		{
			name: "resume from blocked", setup: []string{"block"}, wantPhase: PhaseActive, wantArm: true, wantDelta: 1,
			run: func(s *Service, g *Goal) (*Goal, error) { return s.Resume(g.Ref()) },
		},
		{
			name: "complete", wantPhase: PhaseComplete, wantDelta: 1,
			run: func(s *Service, g *Goal) (*Goal, error) { return s.Complete(g.Ref()) },
		},
		{
			name: "block", wantPhase: PhaseBlocked, wantDelta: 1,
			run: func(s *Service, g *Goal) (*Goal, error) {
				return s.Block(g.Ref(), BlockerModelReported, "needs a decision")
			},
		},
		{
			name: "pause a paused goal", setup: []string{"pause"}, errCode: ErrIllegalPhase,
			run: func(s *Service, g *Goal) (*Goal, error) { return s.Pause(g.Ref()) },
		},
		{
			name: "complete a completed goal", setup: []string{"complete"}, errCode: ErrIllegalPhase,
			run: func(s *Service, g *Goal) (*Goal, error) { return s.Complete(g.Ref()) },
		},
		{
			name: "resume a completed goal", setup: []string{"complete"}, errCode: ErrIllegalPhase,
			run: func(s *Service, g *Goal) (*Goal, error) { return s.Resume(g.Ref()) },
		},
		{
			name: "edit a completed goal", setup: []string{"complete"}, errCode: ErrIllegalPhase,
			run: func(s *Service, g *Goal) (*Goal, error) { return s.Edit(g.Ref(), "again") },
		},
		{
			name: "block a completed goal", setup: []string{"complete"}, errCode: ErrIllegalPhase,
			run: func(s *Service, g *Goal) (*Goal, error) {
				return s.Block(g.Ref(), BlockerModelReported, "late")
			},
		},
		{
			name: "block without a code", errCode: ErrInvalid,
			run: func(s *Service, g *Goal) (*Goal, error) { return s.Block(g.Ref(), " ", "why") },
		},
	}

	apply := func(t *testing.T, s *Service, op string) {
		t.Helper()
		g := snapshot(t, s)
		var err error
		switch op {
		case "pause":
			_, err = s.Pause(g.Ref())
		case "block":
			_, err = s.Block(g.Ref(), BlockerModelReported, "setup")
		case "complete":
			_, err = s.Complete(g.Ref())
		default:
			t.Fatalf("unknown setup op %q", op)
		}
		if err != nil {
			t.Fatalf("setup %q: %v", op, err)
		}
	}

	for _, tc := range steps {
		t.Run(tc.name, func(t *testing.T) {
			s := testService()
			if _, err := s.Create("the objective", 8); err != nil {
				t.Fatalf("Create: %v", err)
			}
			for _, op := range tc.setup {
				apply(t, s, op)
			}
			before := snapshot(t, s)
			got, err := tc.run(s, before)
			if tc.errCode != "" {
				if err == nil {
					t.Fatalf("%q succeeded, want %s", tc.name, tc.errCode)
				}
				requireCode(t, err, tc.errCode)
				now := snapshot(t, s)
				if now.Phase != before.Phase || now.Revision != before.Revision {
					t.Errorf("a refused %q changed the goal: %+v -> %+v", tc.name, before, now)
				}
				return
			}
			if err != nil {
				t.Fatalf("%q: %v", tc.name, err)
			}
			if got.Phase != tc.wantPhase {
				t.Errorf("phase = %s, want %s", got.Phase, tc.wantPhase)
			}
			if s.Armed() != tc.wantArm {
				t.Errorf("armed = %v, want %v", s.Armed(), tc.wantArm)
			}
			if delta := got.Revision - before.Revision; delta != tc.wantDelta {
				t.Errorf("revision moved by %d, want %d", delta, tc.wantDelta)
			}
		})
	}
}

func TestStaleRevisionIsRefused(t *testing.T) {
	s := testService()
	g, err := s.Create("the objective", 8)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	stale := g.Ref()
	if _, err := s.Edit(stale, "newer objective"); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if _, err := s.Pause(stale); err == nil {
		t.Fatal("a mutation on an older revision must be refused")
	} else {
		requireCode(t, err, ErrStaleRevision)
	}
}

// TestResumeRearmsAnActiveGoal is the "a human said continue" path after a
// resume or a fork: the durable phase is already active, so only the
// process-local permission changes and the revision must not move.
func TestResumeRearmsAnActiveGoal(t *testing.T) {
	s := testService()
	created, err := s.Create("the objective", 8)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.Restore(created.Clone()); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if s.Armed() {
		t.Fatal("a restored goal must be disarmed")
	}

	restored := snapshot(t, s)
	rearmed, err := s.Resume(restored.Ref())
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if !s.Armed() {
		t.Error("Resume must arm the goal")
	}
	if rearmed.Revision != restored.Revision {
		t.Errorf("rearming changed the revision: %d -> %d", restored.Revision, rearmed.Revision)
	}
	if rearmed.Phase != PhaseActive {
		t.Errorf("phase = %s, want active", rearmed.Phase)
	}
}

func TestResumeRefusesOnceTheRoundsAreSpent(t *testing.T) {
	s := testService()
	if _, err := s.Create("the objective", 1); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.StartRound(); err != nil {
		t.Fatalf("StartRound: %v", err)
	}
	if _, err := s.Pause(snapshot(t, s).Ref()); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if _, err := s.Resume(snapshot(t, s).Ref()); err == nil {
		t.Fatal("resuming at the round cap must be refused")
	} else {
		requireCode(t, err, ErrRoundLimit)
	}
}

func TestStartRound(t *testing.T) {
	t.Run("counts to the cap", func(t *testing.T) {
		s := testService()
		if _, err := s.Create("the objective", 2); err != nil {
			t.Fatalf("Create: %v", err)
		}
		first, err := s.StartRound()
		if err != nil || first != 1 {
			t.Fatalf("first round = %d, %v; want 1, nil", first, err)
		}
		second, err := s.StartRound()
		if err != nil || second != 2 {
			t.Fatalf("second round = %d, %v; want 2, nil", second, err)
		}
		if got := snapshot(t, s).RoundsStarted; got != 2 {
			t.Errorf("rounds_started = %d, want 2", got)
		}
		if _, err := s.StartRound(); err == nil {
			t.Fatal("a round beyond the cap must be refused")
		} else {
			requireCode(t, err, ErrRoundLimit)
		}
	})

	t.Run("needs a goal", func(t *testing.T) {
		if _, err := testService().StartRound(); err == nil {
			t.Fatal("a round with no goal must be refused")
		} else {
			requireCode(t, err, ErrNoGoal)
		}
	})

	t.Run("needs an active goal", func(t *testing.T) {
		s := testService()
		if _, err := s.Create("the objective", 4); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := s.Pause(snapshot(t, s).Ref()); err != nil {
			t.Fatalf("Pause: %v", err)
		}
		if _, err := s.StartRound(); err == nil {
			t.Fatal("a paused goal must not start a round")
		} else {
			requireCode(t, err, ErrIllegalPhase)
		}
	})

	t.Run("needs to be armed", func(t *testing.T) {
		s := testService()
		created, err := s.Create("the objective", 4)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := s.Restore(created.Clone()); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		if _, err := s.StartRound(); err == nil {
			t.Fatal("a disarmed goal must not start a round")
		} else {
			requireCode(t, err, ErrDisarmed)
		}
	})
}

func TestShouldContinue(t *testing.T) {
	t.Run("no goal", func(t *testing.T) {
		if ok, reason := testService().ShouldContinue(); ok || reason == "" {
			t.Errorf("ShouldContinue() = %v, %q; want false with a reason", ok, reason)
		}
	})
	t.Run("active and armed", func(t *testing.T) {
		s := testService()
		if _, err := s.Create("the objective", 4); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if ok, reason := s.ShouldContinue(); !ok {
			t.Errorf("ShouldContinue() = false, %q; want true", reason)
		}
	})
	t.Run("paused", func(t *testing.T) {
		s := testService()
		if _, err := s.Create("the objective", 4); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := s.Pause(snapshot(t, s).Ref()); err != nil {
			t.Fatalf("Pause: %v", err)
		}
		if ok, _ := s.ShouldContinue(); ok {
			t.Error("a paused goal must not continue")
		}
	})
	t.Run("disarmed", func(t *testing.T) {
		s := testService()
		created, err := s.Create("the objective", 4)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if err := s.Restore(created.Clone()); err != nil {
			t.Fatalf("Restore: %v", err)
		}
		if ok, _ := s.ShouldContinue(); ok {
			t.Error("a disarmed goal must not continue")
		}
	})
	t.Run("cap reached", func(t *testing.T) {
		s := testService()
		if _, err := s.Create("the objective", 1); err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := s.StartRound(); err != nil {
			t.Fatalf("StartRound: %v", err)
		}
		if ok, _ := s.ShouldContinue(); ok {
			t.Error("a goal at its round cap must not continue")
		}
	})
	t.Run("blocked", func(t *testing.T) {
		s := testService()
		created, err := s.Create("the objective", 4)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if _, err := s.Block(created.Ref(), BlockerModelReported, "why"); err != nil {
			t.Fatalf("Block: %v", err)
		}
		if ok, _ := s.ShouldContinue(); ok {
			t.Error("a blocked goal must not continue")
		}
	})
}

func TestRestoreRejectsAnInvalidGoalAndKeepsTheCurrentOne(t *testing.T) {
	s := testService()
	created, err := s.Create("the objective", 4)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	bad := created.Clone()
	bad.RoundsStarted = bad.MaxGoalRounds + 1
	if err := s.Restore(bad); err == nil {
		t.Fatal("an invalid goal must be refused")
	}
	if got := snapshot(t, s); got.ID != created.ID {
		t.Errorf("a refused restore replaced the goal: %q -> %q", created.ID, got.ID)
	}

	if err := s.Restore(nil); err != nil {
		t.Fatalf("Restore(nil): %v", err)
	}
	if s.Snapshot() != nil || s.Armed() {
		t.Error("Restore(nil) must clear the goal and disarm")
	}
}

func TestClearRemovesTheGoal(t *testing.T) {
	s := testService()
	if _, err := s.Create("the objective", 4); err != nil {
		t.Fatalf("Create: %v", err)
	}
	s.Clear()
	if s.Snapshot() != nil {
		t.Error("Clear must remove the goal")
	}
	if s.Armed() {
		t.Error("Clear must disarm")
	}
	if ok, _ := s.ShouldContinue(); ok {
		t.Error("a cleared goal must not continue")
	}
}
