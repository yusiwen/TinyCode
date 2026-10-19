// Package goal holds the one completion objective a session can be working
// toward: its durable phase, its round budget, and the process-local permission
// to continue it without a human.
//
// The split is deliberate. Everything a restart has to restore lives in Goal and
// is persisted with the session. Whether work may continue *on its own* is not
// persisted: every start, resume and fork begins disarmed, so a goal cannot
// revive itself while nobody is watching. Service owns both halves and is the
// only writer of either.
package goal

import (
	"fmt"
	"strings"
	"time"
)

// Phase is the durable lifecycle phase of a goal. A goal is in exactly one of
// them, and the phase — not the conversation — is what a resumed session
// reports.
type Phase string

const (
	// PhaseActive means the objective is being worked on.
	PhaseActive Phase = "active"
	// PhasePaused means a human (or a cancelled round) stopped continuation.
	PhasePaused Phase = "paused"
	// PhaseBlocked means the goal stopped for a named, recorded reason.
	PhaseBlocked Phase = "blocked"
	// PhaseComplete means the objective is achieved.
	PhaseComplete Phase = "complete"
)

// Valid reports whether p is one of the four phases.
func (p Phase) Valid() bool {
	switch p {
	case PhaseActive, PhasePaused, PhaseBlocked, PhaseComplete:
		return true
	}
	return false
}

// Blocker codes are stable: a reader can act on the code without parsing the
// reason, and a later condition cannot silently reuse one for something else.
const (
	// BlockerRoundLimit is recorded when a goal spent its whole round budget.
	BlockerRoundLimit = "round-limit"
	// BlockerNoProgress is recorded when consecutive rounds produced no work.
	BlockerNoProgress = "no-progress"
	// BlockerModelReported is recorded when the model named a concrete blocker.
	BlockerModelReported = "model-reported"
)

// DefaultMaxRounds is the cap a create gets when it names none. One round is a
// whole turn — dozens of tool calls — so this bounds *continuation*, not the
// cost of a single round; the token budgets are what bound money.
const DefaultMaxRounds = 256

// Blocker records why a goal stopped.
type Blocker struct {
	Code   string `json:"code"`
	Reason string `json:"reason,omitempty"`
}

// Goal is one completion objective: the durable half of the capability.
//
// It carries no activation flag, on purpose. "May this continue while nobody is
// watching?" is process-local state held by Service, so a restart, a resume and
// a fork all begin disarmed without anything having to remember to reset it.
type Goal struct {
	ID            string    `json:"id"`
	Objective     string    `json:"objective"`
	Phase         Phase     `json:"phase"`
	RoundsStarted int       `json:"rounds_started"`
	MaxGoalRounds int       `json:"max_goal_rounds"`
	Blocker       *Blocker  `json:"blocker,omitempty"`
	Revision      int       `json:"revision"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Ref is the exact identity a mutation was decided on. A mutation carrying an
// older revision is refused rather than overwriting a newer state, because a
// model tool call and the continuation driver can both reach the same goal.
type Ref struct {
	ID       string `json:"id"`
	Revision int    `json:"revision"`
}

// Ref returns the identity a caller must echo back to mutate this goal.
func (g *Goal) Ref() Ref {
	return Ref{ID: g.ID, Revision: g.Revision}
}

// Clone returns a deep copy, so a reader can hold a view while the goal moves
// on. A nil goal clones to nil.
func (g *Goal) Clone() *Goal {
	if g == nil {
		return nil
	}
	clone := *g
	if g.Blocker != nil {
		blocker := *g.Blocker
		clone.Blocker = &blocker
	}
	return &clone
}

// RoundsLeft is how much continuation budget remains.
func (g *Goal) RoundsLeft() int {
	if g == nil {
		return 0
	}
	left := g.MaxGoalRounds - g.RoundsStarted
	if left < 0 {
		return 0
	}
	return left
}

// Validate reports whether every field is one a loaded file may legally carry.
// It runs when a goal is restored, so a hand-edited or truncated session file is
// refused with a reason instead of driving rounds from a nonsense state.
func (g *Goal) Validate() error {
	if g == nil {
		return fmt.Errorf("goal is missing")
	}
	if strings.TrimSpace(g.ID) == "" {
		return fmt.Errorf("goal: id is required")
	}
	if strings.TrimSpace(g.Objective) == "" {
		return fmt.Errorf("goal %q: objective is required", g.ID)
	}
	if !g.Phase.Valid() {
		return fmt.Errorf("goal %q: unknown phase %q", g.ID, g.Phase)
	}
	if g.MaxGoalRounds <= 0 {
		return fmt.Errorf("goal %q: max_goal_rounds must be positive, got %d", g.ID, g.MaxGoalRounds)
	}
	if g.RoundsStarted < 0 || g.RoundsStarted > g.MaxGoalRounds {
		return fmt.Errorf("goal %q: rounds_started %d is outside 0..%d", g.ID, g.RoundsStarted, g.MaxGoalRounds)
	}
	if g.Revision < 1 {
		return fmt.Errorf("goal %q: revision must be at least 1, got %d", g.ID, g.Revision)
	}
	if g.Phase == PhaseBlocked {
		if g.Blocker == nil {
			return fmt.Errorf("goal %q: a blocked goal needs a blocker", g.ID)
		}
		if strings.TrimSpace(g.Blocker.Code) == "" {
			return fmt.Errorf("goal %q: the blocker needs a code", g.ID)
		}
	} else if g.Blocker != nil {
		return fmt.Errorf("goal %q: a %s goal must not carry a blocker", g.ID, g.Phase)
	}
	return nil
}
