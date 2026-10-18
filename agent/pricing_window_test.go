package agent

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// The vendor's published pair for the route this repository ships as its default:
// off-peak is half of peak, and peak is 01:00-04:00 and 06:00-10:00 UTC on
// weekdays (docs/pricing-decision.md, read 2026-10-14).
const (
	deepseekOffPeakInput    = 0.15
	deepseekPeakInput       = 0.30
	deepseekOffPeakOutput   = 0.60
	deepseekPeakOutput      = 1.20
	deepseekOffPeakCacheHit = 0.003
	deepseekPeakCacheHit    = 0.006
)

const pricedKey = "deepseek/deepseek-flash"

// peakRates is the published peak set, which every window below carries.
func peakRates() Price {
	return Price{
		InputPerMillion:     deepseekPeakInput,
		OutputPerMillion:    deepseekPeakOutput,
		CacheReadPerMillion: deepseekPeakCacheHit,
	}
}

// offPeakRates is the entry's own set, and half of peak in every lane.
func offPeakRates() Price {
	return Price{
		InputPerMillion:     deepseekOffPeakInput,
		OutputPerMillion:    deepseekOffPeakOutput,
		CacheReadPerMillion: deepseekOffPeakCacheHit,
	}
}

// deepseekPeakTable is config.example.json's declaration in code: off-peak on the
// entry, both peak blocks as windows.
func deepseekPeakTable(t *testing.T) *PriceTable {
	t.Helper()
	table := NewPriceTable("USD")
	if err := table.SetEntry(pricedKey, offPeakRates(), []PriceWindow{
		mustWindow(t, []string{"mon", "tue", "wed", "thu", "fri"}, "01:00", "04:00", peakRates()),
		mustWindow(t, []string{"mon", "tue", "wed", "thu", "fri"}, "06:00", "10:00", peakRates()),
	}); err != nil {
		t.Fatalf("SetEntry: %v", err)
	}
	return table
}

func mustWindow(t *testing.T, days []string, from, to string, price Price) PriceWindow {
	t.Helper()
	w, err := NewWindow(days, from, to, price)
	if err != nil {
		t.Fatalf("NewWindow(%v, %s, %s): %v", days, from, to, err)
	}
	return w
}

// mondayAt is an instant in the first week far enough in the past to be stable.
// 2026-10-19 is a Monday, and the assertion says so rather than trusting a date.
func mondayAt(t *testing.T, hour, minute int) time.Time {
	t.Helper()
	at := time.Date(2026, time.October, 19, hour, minute, 0, 0, time.UTC)
	if at.Weekday() != time.Monday {
		t.Fatalf("%s is not a Monday; this test's instants are off", at.Format(time.DateOnly))
	}
	return at
}

func saturdayAt(t *testing.T, hour, minute int) time.Time {
	t.Helper()
	at := time.Date(2026, time.October, 24, hour, minute, 0, 0, time.UTC)
	if at.Weekday() != time.Saturday {
		t.Fatalf("%s is not a Saturday; this test's instants are off", at.Format(time.DateOnly))
	}
	return at
}

// costAt prices one usage record and reports the amount, failing when the table
// declines to price it — an unknown cost would otherwise read as a zero.
func costAt(t *testing.T, table *PriceTable, at time.Time, usage types.Usage) float64 {
	t.Helper()
	cost, ok := table.AmountAt(pricedKey, usage, at)
	if !ok {
		t.Fatalf("no declared rate priced %+v at %s", usage, at)
	}
	return cost.Amount
}

func closeTo(got, want float64) bool { return math.Abs(got-want) < 1e-12 }

// TestPeakWindowPricesAtItsOwnRates is the first acceptance: inside a window the
// window's rates apply, not the entry's.
func TestPeakWindowPricesAtItsOwnRates(t *testing.T) {
	table := deepseekPeakTable(t)
	// A million prompt tokens, none of them cached: one lane, so the amount is
	// the lane's rate.
	usage := types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}

	if got := costAt(t, table, mondayAt(t, 2, 0), usage); !closeTo(got, deepseekPeakInput) {
		t.Errorf("02:00 on Monday cost %v, want the peak input rate %v", got, deepseekPeakInput)
	}
}

// TestOutsideEveryWindowIsOffPeak is the other half: a minute no window covers
// costs the entry's own rate.
func TestOutsideEveryWindowIsOffPeak(t *testing.T) {
	table := deepseekPeakTable(t)
	usage := types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}

	for _, at := range []time.Time{mondayAt(t, 4, 0), mondayAt(t, 12, 0), mondayAt(t, 0, 30)} {
		if got := costAt(t, table, at, usage); !closeTo(got, deepseekOffPeakInput) {
			t.Errorf("%s cost %v, want the off-peak input rate %v", at.Format(time.TimeOnly), got, deepseekOffPeakInput)
		}
	}
}

// TestWeekendIsOffPeak pins that the windows name weekdays only: a vendor's peak
// schedule does not survive into the weekend, and neither may ours.
func TestWeekendIsOffPeak(t *testing.T) {
	table := deepseekPeakTable(t)
	usage := types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}

	for _, at := range []time.Time{saturdayAt(t, 2, 0), saturdayAt(t, 8, 0)} {
		if got := costAt(t, table, at, usage); !closeTo(got, deepseekOffPeakInput) {
			t.Errorf("%s cost %v, want the off-peak input rate %v", at.Format(time.RFC3339), got, deepseekOffPeakInput)
		}
	}
	// Sunday 02:00 UTC is inside no window for the same reason, on the other day.
	sunday := time.Date(2026, time.October, 25, 2, 0, 0, 0, time.UTC)
	if sunday.Weekday() != time.Sunday {
		t.Fatalf("%s is not a Sunday", sunday.Format(time.DateOnly))
	}
	if got := costAt(t, table, sunday, usage); !closeTo(got, deepseekOffPeakInput) {
		t.Errorf("Sunday 02:00 cost %v, want the off-peak rate %v", got, deepseekOffPeakInput)
	}
}

// TestPeakIsExactlyTwiceOffPeak is the acceptance stated as a ratio: the same
// usage priced at 02:00 and at 12:00 on a Monday differs by exactly two, because
// the vendor's off-peak is exactly half of peak in every lane.
func TestPeakIsExactlyTwiceOffPeak(t *testing.T) {
	table := deepseekPeakTable(t)
	// A mix of lanes, so a factor that only holds for one of them cannot pass.
	usage := types.Usage{
		PromptTokens:       1_000_000,
		CompletionTokens:   500_000,
		TotalTokens:        1_500_000,
		CachedPromptTokens: 400_000,
	}

	offPeak := costAt(t, table, mondayAt(t, 12, 0), usage)
	peak := costAt(t, table, mondayAt(t, 2, 0), usage)
	if offPeak == 0 || peak == 0 {
		t.Fatalf("off-peak = %v, peak = %v; a zero amount would make the ratio vacuous", offPeak, peak)
	}
	if got := peak / offPeak; !closeTo(got, 2) {
		t.Errorf("peak/off-peak = %v (%v vs %v), want exactly 2", got, peak, offPeak)
	}
}

// TestEveryDeclaredLaneMatchesTheVendorsPair isolates each lane and holds it
// against the published table, so "twice" cannot be true by a coincidence of
// rounding in one lane while another is wrong.
func TestEveryDeclaredLaneMatchesTheVendorsPair(t *testing.T) {
	table := deepseekPeakTable(t)

	cases := []struct {
		lane          string
		usage         types.Usage
		offPeak, peak float64
	}{
		{
			"input, cache miss",
			types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000},
			deepseekOffPeakInput, deepseekPeakInput,
		},
		{
			"input, cache hit",
			types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000, CachedPromptTokens: 1_000_000},
			deepseekOffPeakCacheHit, deepseekPeakCacheHit,
		},
		{
			"output",
			types.Usage{CompletionTokens: 1_000_000, TotalTokens: 1_000_000},
			deepseekOffPeakOutput, deepseekPeakOutput,
		},
	}

	for _, c := range cases {
		t.Run(c.lane, func(t *testing.T) {
			if got := costAt(t, table, mondayAt(t, 12, 0), c.usage); !closeTo(got, c.offPeak) {
				t.Errorf("off-peak = %v, want %v", got, c.offPeak)
			}
			if got := costAt(t, table, mondayAt(t, 2, 0), c.usage); !closeTo(got, c.peak) {
				t.Errorf("peak = %v, want %v", got, c.peak)
			}
		})
	}
}

// TestWindowBoundaries pins the minute each window starts and stops at: from is
// inclusive, to is exclusive, so 04:00 belongs to the cheaper rate.
func TestWindowBoundaries(t *testing.T) {
	table := deepseekPeakTable(t)
	usage := types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}

	cases := []struct {
		hour, minute int
		want         float64
	}{
		{0, 59, deepseekOffPeakInput},
		{1, 0, deepseekPeakInput},
		{3, 59, deepseekPeakInput},
		{4, 0, deepseekOffPeakInput},
		{6, 0, deepseekPeakInput},
		{9, 59, deepseekPeakInput},
		{10, 0, deepseekOffPeakInput},
	}

	for _, c := range cases {
		at := mondayAt(t, c.hour, c.minute)
		if got := costAt(t, table, at, usage); !closeTo(got, c.want) {
			t.Errorf("%s cost %v, want %v", at.Format(time.TimeOnly), got, c.want)
		}
	}
}

// TestClockIsUTCNotLocal pins that the windows are resolved in UTC whatever zone
// the instant arrives in. Reading the wall clock's own zone instead would shift
// every window by the machine's offset — silently, and only for some users.
func TestClockIsUTCNotLocal(t *testing.T) {
	table := deepseekPeakTable(t)
	usage := types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}

	// The same instant as a UTC time and as a +08:00 time: Monday 02:00 UTC.
	utc := time.Date(2026, time.October, 19, 2, 0, 0, 0, time.UTC)
	plusEight := time.Date(2026, time.October, 19, 10, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	if !utc.Equal(plusEight) {
		t.Fatalf("%s and %s are not the same instant", utc, plusEight)
	}
	if got := costAt(t, table, utc, usage); !closeTo(got, deepseekPeakInput) {
		t.Errorf("02:00 UTC cost %v, want the peak rate %v", got, deepseekPeakInput)
	}
	if got := costAt(t, table, plusEight, usage); !closeTo(got, deepseekPeakInput) {
		t.Errorf("the same instant written in +08:00 cost %v, want the peak rate %v", got, deepseekPeakInput)
	}

	// The control: a wall-clock reading of 02:00 in +08:00 is Sunday 18:00 UTC,
	// which is off-peak. A resolver that used the time's own zone would price it
	// as peak and fail here.
	local := time.Date(2026, time.October, 19, 2, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	if got := costAt(t, table, local, usage); !closeTo(got, deepseekOffPeakInput) {
		t.Errorf("02:00 +08:00 cost %v, want the off-peak rate %v", got, deepseekOffPeakInput)
	}
}

// TestEndOfDayWindow pins the "until midnight" spelling a published schedule uses:
// 00:00 as the end of a day covers 23:59 and stops there.
func TestEndOfDayWindow(t *testing.T) {
	window := mustWindow(t, []string{"mon"}, "10:00", "00:00", Price{InputPerMillion: 0.30})
	if got, want := window.describe(), "mon 10:00-24:00"; got != want {
		t.Fatalf("describe() = %q, want %q", got, want)
	}
	for _, at := range []time.Time{mondayAt(t, 10, 0), mondayAt(t, 23, 59)} {
		if !window.covers(at) {
			t.Errorf("%s is outside a window that runs to midnight", at.Format(time.TimeOnly))
		}
	}
	// Tuesday 00:00 is the next day, and not covered.
	tuesday := time.Date(2026, time.October, 20, 0, 0, 0, 0, time.UTC)
	if window.covers(tuesday) {
		t.Error("Tuesday 00:00 is inside Monday's window")
	}

	// "24:00" is the same declaration, spelled the other way.
	explicit := mustWindow(t, []string{"mon"}, "10:00", "24:00", Price{InputPerMillion: 0.30})
	if explicit.describe() != window.describe() {
		t.Errorf("10:00-24:00 describes as %q, want %q", explicit.describe(), window.describe())
	}
}

// TestAnExactEntrysRatesWinOverAWildcardsWindows pins the resolution order against
// the schedule: a wildcard's windows must not reach a call that an exact key
// priced, or the more specific declaration would be overridden by the looser one.
func TestAnExactEntrysRatesWinOverAWildcardsWindows(t *testing.T) {
	table := NewPriceTable("USD")
	if err := table.SetEntry("deepseek/*", Price{InputPerMillion: 0.10}, []PriceWindow{
		mustWindow(t, []string{"mon", "tue", "wed", "thu", "fri"}, "01:00", "04:00", peakRates()),
	}); err != nil {
		t.Fatalf("SetEntry: %v", err)
	}
	table.Set(pricedKey, Price{InputPerMillion: deepseekOffPeakInput})

	usage := types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}
	if got := costAt(t, table, mondayAt(t, 2, 0), usage); !closeTo(got, deepseekOffPeakInput) {
		t.Errorf("the exact key priced at %v inside the wildcard's window, want its own %v", got, deepseekOffPeakInput)
	}
}

// TestSetDropsDeclaredWindows pins the other direction: a raw rate is one number
// for every instant, so it replaces a schedule rather than sitting beside it.
func TestSetDropsDeclaredWindows(t *testing.T) {
	table := deepseekPeakTable(t)
	table.Set(pricedKey, Price{InputPerMillion: 0.42})

	usage := types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}
	if got := costAt(t, table, mondayAt(t, 2, 0), usage); !closeTo(got, 0.42) {
		t.Errorf("02:00 cost %v after Set, want the rate Set declared (0.42)", got)
	}
}

// TestWindowlessEntryIgnoresTheInstant is the acceptance that an entry with no
// windows behaves exactly as it did before this tier existed.
func TestWindowlessEntryIgnoresTheInstant(t *testing.T) {
	table := NewPriceTable("USD")
	table.Set(pricedKey, offPeakRates())
	usage := types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}

	for _, at := range []time.Time{mondayAt(t, 2, 0), mondayAt(t, 12, 0), saturdayAt(t, 8, 0)} {
		if got := costAt(t, table, at, usage); !closeTo(got, deepseekOffPeakInput) {
			t.Errorf("%s cost %v, want the entry's only rate %v", at.Format(time.RFC3339), got, deepseekOffPeakInput)
		}
	}
}

// TestOverlappingWindowsAreRefused is the acceptance that a shared minute is a
// configuration error naming the windows, not a first-match-wins guess.
func TestOverlappingWindowsAreRefused(t *testing.T) {
	table := NewPriceTable("USD")
	err := table.SetEntry(pricedKey, offPeakRates(), []PriceWindow{
		mustWindow(t, []string{"mon", "tue", "wed", "thu", "fri"}, "01:00", "04:00", peakRates()),
		mustWindow(t, []string{"mon", "tue", "wed", "thu", "fri"}, "03:00", "06:00", peakRates()),
	})
	if err == nil {
		t.Fatal("overlapping windows were accepted")
	}
	for _, want := range []string{"mon-fri 01:00-04:00", "mon-fri 03:00-06:00", "overlap on mon"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}

// TestWholeWeekIsRefused pins that the entry's own rates have to stay reachable:
// a schedule that covers everything means the base rates were a mistake.
func TestWholeWeekIsRefused(t *testing.T) {
	table := NewPriceTable("USD")
	err := table.SetEntry(pricedKey, Price{InputPerMillion: deepseekOffPeakInput}, []PriceWindow{
		mustWindow(t, []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, "00:00", "24:00",
			Price{InputPerMillion: deepseekPeakInput}),
	})
	if err == nil {
		t.Fatal("a schedule covering the whole week was accepted")
	}
	if !strings.Contains(err.Error(), "cover every minute of the week") {
		t.Errorf("refusal %q does not say the entry's own rates became unreachable", err)
	}
}

// TestMissingLaneIsRefused pins the rule that a window repeats every lane the
// entry declares. A window that leaves one at zero would charge the entry's rate
// inside the window — half price, silently, in exactly one lane.
func TestMissingLaneIsRefused(t *testing.T) {
	table := NewPriceTable("USD")
	err := table.SetEntry(pricedKey, offPeakRates(), []PriceWindow{
		mustWindow(t, []string{"mon"}, "01:00", "04:00", Price{
			InputPerMillion:  deepseekPeakInput,
			OutputPerMillion: deepseekPeakOutput,
			// cache_read_per_million is missing on purpose.
		}),
	})
	if err == nil {
		t.Fatal("a window that omits a declared lane was accepted")
	}
	if !strings.Contains(err.Error(), "cache_read_per_million") {
		t.Errorf("refusal %q does not name the lane it dropped", err)
	}
}

// TestRefusedEntryLeavesTheTableEmpty is the acceptance that the failure mode is
// "unknown", not "priced by whichever window came first": a refused declaration
// stores nothing, so no call is ever priced by it.
func TestRefusedEntryLeavesTheTableEmpty(t *testing.T) {
	table := NewPriceTable("USD")
	err := table.SetEntry(pricedKey, offPeakRates(), []PriceWindow{
		mustWindow(t, []string{"mon"}, "01:00", "04:00", peakRates()),
		mustWindow(t, []string{"mon"}, "02:00", "03:00", peakRates()),
	})
	if err == nil {
		t.Fatal("the overlapping declaration was accepted")
	}
	if got := table.Len(); got != 0 {
		t.Errorf("the table holds %d keys after a refusal, want 0", got)
	}
	if _, ok := table.AmountAt(pricedKey, types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}, mondayAt(t, 2, 0)); ok {
		t.Error("a refused declaration priced a call instead of leaving it unknown")
	}

	// The control: the same table still accepts a declaration that is fine, so the
	// emptiness above is about the refusal and not about a table that stores
	// nothing at all.
	if err := table.SetEntry("ollama/llama3", Price{InputPerMillion: 0.10}, nil); err != nil {
		t.Fatalf("a windowless entry was refused: %v", err)
	}
	if got := table.Len(); got != 1 {
		t.Errorf("the table holds %d keys after the control entry, want 1", got)
	}
}

// TestNewWindowRefusesWhatItCannotSchedule collects the malformed declarations,
// each with the phrase its refusal has to carry: the reader has to be able to fix
// it from the message alone.
func TestNewWindowRefusesWhatItCannotSchedule(t *testing.T) {
	cases := []struct {
		name     string
		days     []string
		from, to string
		want     string
	}{
		{"minute out of range", []string{"mon"}, "01:60", "04:00", "not a time of day"},
		{"hour out of range", []string{"mon"}, "25:00", "26:00", "not a time of day"},
		{"minute not two digits", []string{"mon"}, "1:5", "04:00", "is not HH:MM"},
		{"signed hour", []string{"mon"}, "+1:00", "04:00", "is not HH:MM"},
		{"not a clock at all", []string{"mon"}, "one", "04:00", "is not HH:MM"},
		{"starts at end of day", []string{"mon"}, "24:00", "04:00", "starts at 24:00"},
		{"crosses midnight", []string{"mon"}, "10:00", "09:00", "two windows"},
		{"covers no time", []string{"mon"}, "10:00", "10:00", "covers no time"},
		{"range instead of days", []string{"mon-fri"}, "01:00", "04:00", "unknown weekday"},
		{"no weekdays", nil, "01:00", "04:00", "no weekdays"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := NewWindow(c.days, c.from, c.to, Price{})
			if err == nil {
				t.Fatalf("%v %s-%s was accepted", c.days, c.from, c.to)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("refusal %q does not say %q", err, c.want)
			}
		})
	}
}

// TestNewWindowAcceptsDayNames pins the spellings a reader may reasonably write,
// and how they read back in a refusal.
func TestNewWindowAcceptsDayNames(t *testing.T) {
	window, err := NewWindow([]string{"Mon", "TUESDAY", "weds"}, "00:00", "24:00", Price{InputPerMillion: 0.1})
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if got, want := window.describe(), "mon-wed 00:00-24:00"; got != want {
		t.Errorf("describe() = %q, want %q", got, want)
	}

	all, err := NewWindow([]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}, "00:00", "24:00", Price{})
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	if got, want := all.describe(), "every day 00:00-24:00"; got != want {
		t.Errorf("describe() = %q, want %q", got, want)
	}
}

// TestCallIsPricedAtItsStartNotItsEnd is the wiring half of the clock: the price
// is resolved at the instant the request is sent, and the clock is read once per
// call. The mock's clock is rigged to answer differently on a second read, so a
// recordCall that asked again — after the response — would price at 04:00 and fail.
func TestCallIsPricedAtItsStartNotItsEnd(t *testing.T) {
	table := deepseekPeakTable(t)
	start := mondayAt(t, 3, 59) // one minute inside the peak block
	end := mondayAt(t, 4, 0)    // one minute past it

	var events []types.CostEvent
	reads := 0
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{
				Content: "answer",
				Usage:   &types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000},
			}, nil
		},
	}
	a := &Agent{
		Provider: &routeMockProvider{MockProvider: provider, route: "deepseek", model: "deepseek-flash"},
		MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test",
		PriceTable: table,
		Now: func() time.Time {
			reads++
			if reads > 1 {
				return end
			}
			return start
		},
		StreamCallbacks: &types.StreamCallbacks{
			OnCost: func(ev types.CostEvent) { events = append(events, ev) },
		},
	}

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if reads != 1 {
		t.Errorf("the clock was read %d times for one provider call, want 1 (at its start)", reads)
	}
	if len(events) != 1 {
		t.Fatalf("OnCost fired %d times, want 1", len(events))
	}
	if events[0].Source != types.CostDeclared {
		t.Fatalf("source = %q, want %q", events[0].Source, types.CostDeclared)
	}
	if !closeTo(events[0].Cost.Amount, deepseekPeakInput) {
		t.Errorf("amount = %v, want the peak rate %v for a call started at 03:59", events[0].Cost.Amount, deepseekPeakInput)
	}
}
