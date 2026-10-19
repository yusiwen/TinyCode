package goal

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Error codes. They are stable so a command, a tool or a test can act on the
// code instead of matching English that drifts.
const (
	ErrNoGoal        = "no-goal"
	ErrUnfinished    = "unfinished-goal"
	ErrStaleRevision = "stale-revision"
	ErrIllegalPhase  = "illegal-phase"
	ErrRoundLimit    = "round-limit"
	ErrDisarmed      = "disarmed"
	ErrInvalid       = "invalid"
)

// Error is a rejected goal operation: a stable code plus a sentence for a human.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func newError(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Service owns the current goal and the process-local permission to continue it.
//
// It never touches the filesystem: the session file is written by the TUI on its
// own goroutine, so there is exactly one writer and a tool call cannot race the
// interface into a half-written session. A caller that wants durability calls
// Snapshot and persists it at a safe point.
//
// Every mutation is compare-and-set on {id, revision}. A model tool call and the
// continuation driver can both reach the same goal, and a lost update would not
// be visible until the next resume.
type Service struct {
	mu    sync.Mutex
	goal  *Goal
	armed bool

	// Now and NewID are seams for tests. Nil means the real clock and a
	// timestamp-based id.
	Now   func() time.Time
	NewID func(time.Time) string

	// DefaultRounds is the cap a create gets when it names none. Zero means
	// DefaultMaxRounds; a host sets it from its configuration so the /goal
	// command and the goal tool cannot disagree about the budget.
	DefaultRounds int
}

// NewService returns a service with no goal, disarmed.
func NewService() *Service {
	return &Service{}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) newID(now time.Time) string {
	if s.NewID != nil {
		return s.NewID(now)
	}
	return fmt.Sprintf("goal-%d", now.UnixNano())
}

// Snapshot returns a detached copy of the current goal, or nil when none is
// current. The caller may read it while the goal moves on.
func (s *Service) Snapshot() *Goal {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.goal.Clone()
}

// Armed reports whether automatic continuation is permitted right now.
func (s *Service) Armed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.armed
}

// Restore adopts the goal a session was saved with. It always disarms, which is
// what makes "a resumed session does not continue on its own" a property of the
// code rather than a promise: there is no path from a loaded file to an armed
// service.
func (s *Service) Restore(g *Goal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if g == nil {
		s.goal = nil
		s.armed = false
		return nil
	}
	if err := g.Validate(); err != nil {
		return err
	}
	s.goal = g.Clone()
	s.armed = false
	return nil
}

// defaultRounds is the configured cap, or the package default.
func (s *Service) defaultRounds() int {
	if s.DefaultRounds > 0 {
		return s.DefaultRounds
	}
	return DefaultMaxRounds
}

// Create starts a new goal and arms it, because a human asked for it.
//
// An unfinished goal must be dealt with first — edited, paused, resumed, blocked
// or cleared — so a session can never quietly abandon one objective for another.
// A completed goal may be replaced directly.
func (s *Service) Create(objective string, maxRounds int) (*Goal, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return nil, newError(ErrInvalid, "an objective is required")
	}
	if maxRounds <= 0 {
		maxRounds = s.defaultRounds()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.goal != nil && s.goal.Phase != PhaseComplete {
		return nil, newError(ErrUnfinished,
			"goal %q is still %s: edit, pause, resume, block or clear it first", s.goal.Objective, s.goal.Phase)
	}
	now := s.now()
	g := &Goal{
		ID:            s.newID(now),
		Objective:     objective,
		Phase:         PhaseActive,
		MaxGoalRounds: maxRounds,
		Revision:      1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	s.goal = g
	s.armed = true
	return g.Clone(), nil
}

// Edit changes the objective without changing the phase. A completed goal is
// refused: that is a new objective, and it should be created as one.
func (s *Service) Edit(ref Ref, objective string) (*Goal, error) {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return nil, newError(ErrInvalid, "an objective is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(ref); err != nil {
		return nil, err
	}
	if s.goal.Phase == PhaseComplete {
		return nil, newError(ErrIllegalPhase, "a completed goal cannot be edited; create a new one")
	}
	s.goal.Objective = objective
	s.touchLocked()
	return s.goal.Clone(), nil
}

// Pause stops automatic continuation and keeps the state.
func (s *Service) Pause(ref Ref) (*Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(ref); err != nil {
		return nil, err
	}
	if s.goal.Phase != PhaseActive {
		return nil, newError(ErrIllegalPhase, "goal is %s, not active", s.goal.Phase)
	}
	s.goal.Phase = PhasePaused
	s.armed = false
	s.touchLocked()
	return s.goal.Clone(), nil
}

// Resume restarts a paused or blocked goal, or rearms an active one after a
// session resume or fork. It refuses once the round budget is spent, because
// there is no round left to run.
func (s *Service) Resume(ref Ref) (*Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(ref); err != nil {
		return nil, err
	}
	if s.goal.RoundsLeft() == 0 {
		return nil, newError(ErrRoundLimit,
			"the goal spent its %d rounds; edit max_goal_rounds before resuming", s.goal.MaxGoalRounds)
	}
	switch s.goal.Phase {
	case PhaseActive:
		// The durable state is already active; only the process-local permission
		// was missing, so nothing durable changes and the revision stays put.
	case PhasePaused, PhaseBlocked:
		s.goal.Phase = PhaseActive
		s.goal.Blocker = nil
		s.touchLocked()
	default:
		return nil, newError(ErrIllegalPhase, "a completed goal cannot be resumed; create a new one")
	}
	s.armed = true
	return s.goal.Clone(), nil
}

// Complete marks the objective achieved and stops continuation.
func (s *Service) Complete(ref Ref) (*Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(ref); err != nil {
		return nil, err
	}
	if s.goal.Phase != PhaseActive && s.goal.Phase != PhasePaused {
		return nil, newError(ErrIllegalPhase, "goal is %s and cannot be completed", s.goal.Phase)
	}
	s.goal.Phase = PhaseComplete
	s.goal.Blocker = nil
	s.armed = false
	s.touchLocked()
	return s.goal.Clone(), nil
}

// Block records a stable blocker code and stops continuation. It is the one
// phase that keeps a reason, so provider limits, spent budgets and requests for
// a human share one durable state instead of multiplying phases.
func (s *Service) Block(ref Ref, code, reason string) (*Goal, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, newError(ErrInvalid, "a blocker code is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkLocked(ref); err != nil {
		return nil, err
	}
	if s.goal.Phase != PhaseActive && s.goal.Phase != PhasePaused {
		return nil, newError(ErrIllegalPhase, "goal is %s and cannot be blocked", s.goal.Phase)
	}
	s.goal.Phase = PhaseBlocked
	s.goal.Blocker = &Blocker{Code: code, Reason: strings.TrimSpace(reason)}
	s.armed = false
	s.touchLocked()
	return s.goal.Clone(), nil
}

// Clear removes the current goal and disarms. The transcript keeps whatever the
// goal's rounds did; this package keeps no history of its own.
func (s *Service) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goal = nil
	s.armed = false
}

// StartRound claims the next round and returns its 1-based number.
//
// The caller must persist the result before doing work: the round is counted
// when it *starts*, so a crash loop cannot spend the budget twice. A round that
// is refused (no goal, not active, disarmed, cap reached) returns an Error and
// consumes nothing.
func (s *Service) StartRound() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.continueLocked(); err != nil {
		return 0, err
	}
	s.goal.RoundsStarted++
	s.touchLocked()
	return s.goal.RoundsStarted, nil
}

// ShouldContinue reports whether the driver may start another round, with a
// reason when it may not. It is the read-only twin of StartRound, for a caller
// that wants to decide before it acts.
func (s *Service) ShouldContinue() (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.continueLocked(); err != nil {
		return false, err.Message
	}
	return true, ""
}

// checkLocked verifies the caller's view is the current one.
func (s *Service) checkLocked(ref Ref) *Error {
	if s.goal == nil {
		return newError(ErrNoGoal, "no goal is current")
	}
	if s.goal.ID != ref.ID || s.goal.Revision != ref.Revision {
		return newError(ErrStaleRevision,
			"goal %s revision %d is not current (revision %d); read it again",
			ref.ID, ref.Revision, s.goal.Revision)
	}
	return nil
}

// continueLocked answers whether a round may start.
func (s *Service) continueLocked() *Error {
	switch {
	case s.goal == nil:
		return newError(ErrNoGoal, "no goal is current")
	case s.goal.Phase != PhaseActive:
		return newError(ErrIllegalPhase, "goal is %s, not active", s.goal.Phase)
	case !s.armed:
		return newError(ErrDisarmed, "continuation is disarmed; a human has to resume the goal")
	case s.goal.RoundsLeft() == 0:
		return newError(ErrRoundLimit, "the goal spent its %d rounds", s.goal.MaxGoalRounds)
	}
	return nil
}

// touchLocked records a durable change: a new revision and a new timestamp. The
// caller must hold the lock.
func (s *Service) touchLocked() {
	s.goal.Revision++
	s.goal.UpdatedAt = s.now()
}
