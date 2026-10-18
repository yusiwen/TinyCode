package main

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/agent"
	"github.com/yusiwen/tinycode/config"
	"github.com/yusiwen/tinycode/types"
)

const pricedRoute = "deepseek/deepseek-flash"

// TestShippedExamplePricesBothHalvesOfTheDay is issue #225's acceptance applied to
// the file the repository tells a reader to copy: the shipped declaration prices
// the default route correctly on both sides of its peak boundary, lane by lane,
// at fixed instants. Before this tier the example could only be right for one half
// of the week — it declared the peak rates, 2x too high for about 79% of it.
func TestShippedExamplePricesBothHalvesOfTheDay(t *testing.T) {
	cfg := loadShippedExampleConfig(t)
	table, err := priceTable(&cfg)
	if err != nil {
		t.Fatalf("priceTable: %v", err)
	}
	if table == nil {
		t.Fatal("the shipped example declares no prices")
	}

	// 2026-10-19 is a Monday: 02:00 UTC is inside the first peak window, 12:00 UTC
	// is outside every window, and the assertion below says the date is what this
	// test thinks it is.
	peak := time.Date(2026, time.October, 19, 2, 0, 0, 0, time.UTC)
	offPeak := time.Date(2026, time.October, 19, 12, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{peak, offPeak} {
		if at.Weekday() != time.Monday {
			t.Fatalf("%s is not a Monday; the instants this test prices are off", at.Format(time.DateOnly))
		}
	}

	cases := []struct {
		lane          string
		usage         types.Usage
		offPeak, peak float64
	}{
		{"input, cache miss", types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000}, 0.15, 0.30},
		{"input, cache hit", types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000, CachedPromptTokens: 1_000_000}, 0.003, 0.006},
		{"output", types.Usage{CompletionTokens: 1_000_000, TotalTokens: 1_000_000}, 0.60, 1.20},
		// DeepSeek charges nothing separate for writing a cache, so the lane falls
		// back to the input rate — of the window it is in, not of the entry.
		{"cache write falls back to input", types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000, CacheWriteTokens: 1_000_000}, 0.15, 0.30},
		{"reasoning falls back to output", types.Usage{CompletionTokens: 1_000_000, TotalTokens: 1_000_000, ReasoningTokens: 1_000_000}, 0.60, 1.20},
	}

	for _, c := range cases {
		t.Run(c.lane, func(t *testing.T) {
			if got := amountOf(t, table, c.usage, offPeak); !almost(got, c.offPeak) {
				t.Errorf("off-peak = %v, want %v", got, c.offPeak)
			}
			if got := amountOf(t, table, c.usage, peak); !almost(got, c.peak) {
				t.Errorf("peak = %v, want %v", got, c.peak)
			}
		})
	}

	// And the ratio the vendor publishes, on a record that uses several lanes at
	// once, so a declaration that doubles only some of them cannot pass.
	mixed := types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_500_000, CompletionTokens: 500_000, CachedPromptTokens: 400_000}
	off := amountOf(t, table, mixed, offPeak)
	on := amountOf(t, table, mixed, peak)
	if off == 0 {
		t.Fatal("the off-peak amount is zero, which would make the ratio vacuous")
	}
	if got := on / off; !almost(got, 2) {
		t.Errorf("peak/off-peak = %v (%v vs %v), want exactly 2", got, on, off)
	}
}

// TestPriceTableRefusesAMalformedWindow is the acceptance that a shared minute is
// a startup error naming the entry and both windows, not a first-match-wins guess.
// The table comes back nil beside the error, so nothing is priced by a schedule
// nobody could defend.
func TestPriceTableRefusesAMalformedWindow(t *testing.T) {
	cfg := &config.Config{Pricing: &config.PricingConfig{
		Currency: "USD",
		Prices: map[string]config.PriceEntry{
			pricedRoute: {
				InputPerMillion: 0.15,
				Windows: []config.PriceWindowEntry{
					{Weekdays: []string{"mon"}, From: "01:00", To: "04:00", InputPerMillion: 0.30},
					{Weekdays: []string{"mon"}, From: "03:00", To: "06:00", InputPerMillion: 0.30},
				},
			},
		},
	}}

	table, err := priceTable(cfg)
	if err == nil {
		t.Fatal("an overlapping schedule was accepted")
	}
	if table != nil {
		t.Error("the table came back non-nil beside an error; nothing may be priced from a refused set")
	}
	for _, want := range []string{pricedRoute, "mon 01:00-04:00", "mon 03:00-06:00", "overlap on mon"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not name %q", err, want)
		}
	}
}

// TestPriceTableRefusesAnUnparseableWindow is the other half of the refusal: a
// declaration that never becomes a window at all. The clock is spelled the way its
// author would type it and is still not HH:MM, so the message has to quote what
// they wrote — a refused schedule must be fixable from the message alone.
func TestPriceTableRefusesAnUnparseableWindow(t *testing.T) {
	cases := []struct {
		name   string
		window config.PriceWindowEntry
		want   []string
	}{
		{
			// A one-digit hour is accepted ("1:00" is how a person writes it); a
			// one-digit minute is not a clock reading in HH:MM.
			"one-digit minute",
			config.PriceWindowEntry{Weekdays: []string{"mon"}, From: "1:0", To: "04:00", InputPerMillion: 0.30},
			[]string{pricedRoute, "1:0", "is not HH:MM"},
		},
		{
			// A weekday range is the compact form a published schedule uses; this
			// schema spells days as a list, so the refusal names the accepted form.
			"range instead of days",
			config.PriceWindowEntry{Weekdays: []string{"mon-fri"}, From: "01:00", To: "04:00", InputPerMillion: 0.30},
			[]string{pricedRoute, "mon-fri", "unknown weekday"},
		},
		{
			"crossing midnight",
			config.PriceWindowEntry{Weekdays: []string{"mon"}, From: "22:00", To: "02:00", InputPerMillion: 0.30},
			[]string{pricedRoute, "two windows"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &config.Config{Pricing: &config.PricingConfig{
				Currency: "USD",
				Prices: map[string]config.PriceEntry{
					pricedRoute: {InputPerMillion: 0.15, Windows: []config.PriceWindowEntry{c.window}},
				},
			}}

			table, err := priceTable(cfg)
			if err == nil {
				t.Fatalf("%+v was accepted", c.window)
			}
			if table != nil {
				t.Error("the table came back non-nil beside an error; nothing may be priced from a refused set")
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not name %q", err, want)
				}
			}
		})
	}
}

// TestPriceTableWiresEachLaneToItsOwnName gives every lane a different number in
// both spellings — the entry's and the window's — so a field wired to the wrong
// lane prices a wrong amount instead of a plausible one. The configuration
// mirrors the agent's structs field by field, which is exactly where such a swap
// would live.
func TestPriceTableWiresEachLaneToItsOwnName(t *testing.T) {
	cfg := &config.Config{Pricing: &config.PricingConfig{
		Currency: "USD",
		Prices: map[string]config.PriceEntry{
			pricedRoute: {
				InputPerMillion:      1,
				OutputPerMillion:     2,
				CacheReadPerMillion:  3,
				CacheWritePerMillion: 4,
				ReasoningPerMillion:  5,
				Windows: []config.PriceWindowEntry{{
					Weekdays:             []string{"mon"},
					From:                 "00:00",
					To:                   "24:00",
					InputPerMillion:      6,
					OutputPerMillion:     7,
					CacheReadPerMillion:  8,
					CacheWritePerMillion: 9,
					ReasoningPerMillion:  10,
				}},
			},
		},
	}}

	table, err := priceTable(cfg)
	if err != nil {
		t.Fatalf("priceTable: %v", err)
	}

	cases := []struct {
		lane          string
		usage         types.Usage
		offPeak, peak float64
	}{
		{"input", types.Usage{PromptTokens: 1_000_000}, 1, 6},
		{"output", types.Usage{CompletionTokens: 1_000_000}, 2, 7},
		{"cache read", types.Usage{PromptTokens: 1_000_000, CachedPromptTokens: 1_000_000}, 3, 8},
		{"cache write", types.Usage{PromptTokens: 1_000_000, CacheWriteTokens: 1_000_000}, 4, 9},
		{"reasoning", types.Usage{CompletionTokens: 1_000_000, ReasoningTokens: 1_000_000}, 5, 10},
	}
	monday := time.Date(2026, time.October, 19, 12, 0, 0, 0, time.UTC)
	tuesday := time.Date(2026, time.October, 20, 12, 0, 0, 0, time.UTC)
	if monday.Weekday() != time.Monday || tuesday.Weekday() != time.Tuesday {
		t.Fatalf("the instants this test prices are not Monday and Tuesday")
	}

	for _, c := range cases {
		t.Run(c.lane, func(t *testing.T) {
			if got := amountOf(t, table, c.usage, tuesday); !almost(got, c.offPeak) {
				t.Errorf("outside the window = %v, want the entry's %v", got, c.offPeak)
			}
			if got := amountOf(t, table, c.usage, monday); !almost(got, c.peak) {
				t.Errorf("inside the window = %v, want the window's %v", got, c.peak)
			}
		})
	}
}

// TestPriceTableIsNilWhenNothingIsDeclared pins the contract the startup path
// relies on: an absent section is not an error, and an empty table prices nothing
// rather than pricing zero.
func TestPriceTableIsNilWhenNothingIsDeclared(t *testing.T) {
	cases := map[string]*config.Config{
		"no config":    nil,
		"no section":   {},
		"no prices":    {Pricing: &config.PricingConfig{Currency: "USD"}},
		"empty prices": {Pricing: &config.PricingConfig{Currency: "USD", Prices: map[string]config.PriceEntry{}}},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			table, err := priceTable(cfg)
			if err != nil {
				t.Fatalf("priceTable: %v", err)
			}
			if table != nil {
				t.Errorf("table = %+v, want nil", table)
			}
		})
	}
}

// TestStartupRefusesAMalformedPriceWindow is the wiring half of the refusal: the
// error priceTable returns has to stop the process rather than be printed and
// ignored. `--list-sessions` is the cheapest startup path that reaches the pricing
// step, so this proves the call site without starting a UI.
func TestStartupRefusesAMalformedPriceWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir()) // no project-local config in the way

	dir := filepath.Join(home, ".tinycode")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"pricing":{"currency":"USD","prices":{"deepseek/deepseek-flash":{"input_per_million":0.15,` +
		`"windows":[{"weekdays":["mon"],"from":"01:00","to":"04:00","input_per_million":0.3},` +
		`{"weekdays":["mon"],"from":"03:00","to":"06:00","input_per_million":0.3}]}}}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newRootCmd()
	cmd.SetArgs([]string{"--list-sessions"})
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("a malformed price schedule started the program")
	}
	for _, want := range []string{pricedRoute, "overlap on mon"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("startup refused with %q, which does not name %q", err, want)
		}
	}
}

// loadShippedExampleConfig reads config.example.json through the real loader, with
// $HOME redirected, and fails if the loader complained: a parse failure is
// reported on stderr and absorbed, so the values alone cannot say whether the file
// was read or discarded.
func loadShippedExampleConfig(t *testing.T) config.Config {
	t.Helper()
	data, err := os.ReadFile("config.example.json")
	if err != nil {
		t.Fatalf("read config.example.json: %v", err)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".tinycode")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	previous := os.Stderr
	os.Stderr = write
	cfg := config.LoadConfig()
	os.Stderr = previous
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	message, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if len(message) > 0 {
		t.Fatalf("the shipped example does not load cleanly: %s", message)
	}
	return cfg
}

// amountOf prices one usage record and fails when the table declines to price it:
// an unknown cost would otherwise read as a zero and turn every assertion vacuous.
func amountOf(t *testing.T, table *agent.PriceTable, usage types.Usage, at time.Time) float64 {
	t.Helper()
	cost, ok := table.AmountAt(pricedRoute, usage, at)
	if !ok {
		t.Fatalf("no declared rate priced %+v at %s", usage, at)
	}
	return cost.Amount
}

func almost(got, want float64) bool { return math.Abs(got-want) < 1e-12 }
