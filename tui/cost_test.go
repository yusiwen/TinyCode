package tui

import (
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/types"
)

// TestStatusBarShowsCostOnlyWhenThereIsSome is what keeps the committed frame
// goldens byte-identical: a session with no cost information renders exactly the
// bar it always has.
func TestStatusBarShowsCostOnlyWhenThereIsSome(t *testing.T) {
	m := layoutModel(30)
	m.config = &config.Config{}
	m.sessionTokens = 1200

	if bar := m.renderStatusBar(); strings.Contains(bar, "cost") {
		t.Errorf("bar = %q, want no cost segment before any cost is known", bar)
	}
}

// TestStatusBarShowsCostPerUnit: money carries its unit, and the calls nobody
// could price are shown beside the total rather than folded into it.
func TestStatusBarShowsCostPerUnit(t *testing.T) {
	m := layoutModel(30)
	m.config = &config.Config{}

	m.Update(CostMsg{Event: types.CostEvent{
		Cost:   types.Cost{Amount: 0.95, Currency: "credits"},
		Source: types.CostReported,
	}})
	if bar := m.renderStatusBar(); !strings.Contains(bar, "cost: 0.95 credits") {
		t.Errorf("bar = %q, want the reported cost with its unit", bar)
	}

	// A second unit is accumulated apart, never added to the first.
	m.Update(CostMsg{Event: types.CostEvent{
		Cost:   types.Cost{Amount: 2, Currency: "USD"},
		Source: types.CostDeclared,
	}})
	if bar := m.renderStatusBar(); !strings.Contains(bar, "cost: 0.95 credits, 2 USD") {
		t.Errorf("bar = %q, want both units", bar)
	}

	// Unknown costs are counted, not dropped and not zeroed.
	m.Update(CostMsg{Event: types.CostEvent{Source: types.CostUnknown}})
	m.Update(CostMsg{Event: types.CostEvent{Source: types.CostUnknown}})
	bar := m.renderStatusBar()
	if !strings.Contains(bar, "cost: 0.95 credits, 2 USD (+2 unpriced)") {
		t.Errorf("bar = %q, want the unpriced count beside the total", bar)
	}
	if m.unpricedCalls != 2 {
		t.Errorf("unpricedCalls = %d, want 2", m.unpricedCalls)
	}
}

// TestStatusBarSaysUnknownWhenNothingCouldBePriced: a session whose calls were
// all unpriced must say so, not show nothing and not show zero.
func TestStatusBarSaysUnknownWhenNothingCouldBePriced(t *testing.T) {
	m := layoutModel(30)
	m.config = &config.Config{}

	m.Update(CostMsg{Event: types.CostEvent{Source: types.CostUnknown}})

	bar := m.renderStatusBar()
	if !strings.Contains(bar, "cost: unknown (1 unpriced)") {
		t.Errorf("bar = %q, want an explicit unknown", bar)
	}
	if strings.Contains(bar, "cost: 0") {
		t.Errorf("bar = %q, want no zero: an unpriced call is not a free one", bar)
	}
}

// TestCostEventFromASupersededRunIsIgnored keeps a cancelled run's accounting out
// of the current session's money.
func TestCostEventFromASupersededRunIsIgnored(t *testing.T) {
	m := layoutModel(30)
	m.runActive = true
	m.runID = 7

	m.Update(CostMsg{RunID: 6, Event: types.CostEvent{
		Cost:   types.Cost{Amount: 5, Currency: "USD"},
		Source: types.CostReported,
	}})

	if len(m.sessionCost) != 0 || m.unpricedCalls != 0 {
		t.Errorf("cost=%v unpriced=%d, want a superseded run ignored", m.sessionCost, m.unpricedCalls)
	}
}

// TestResetSessionStatsClearsCost: switching the transcript must not carry
// another conversation's money into this one's bar.
func TestResetSessionStatsClearsCost(t *testing.T) {
	m := layoutModel(30)
	m.Update(CostMsg{Event: types.CostEvent{
		Cost:   types.Cost{Amount: 1, Currency: "USD"},
		Source: types.CostReported,
	}})
	m.Update(CostMsg{Event: types.CostEvent{Source: types.CostUnknown}})

	m.resetSessionStats()

	if len(m.sessionCost) != 0 || m.unpricedCalls != 0 {
		t.Errorf("cost=%v unpriced=%d, want both cleared", m.sessionCost, m.unpricedCalls)
	}
}
