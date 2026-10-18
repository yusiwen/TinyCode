package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestPricingWindowsRoundTripThroughJSON pins the key names a user types for a
// window, and that the entry's own rates survive beside it: the windows are an
// addition to an entry, not a replacement for it (issue #225).
func TestPricingWindowsRoundTripThroughJSON(t *testing.T) {
	raw := []byte(`{"pricing":{"currency":"USD","prices":{"deepseek/deepseek-flash":{` +
		`"input_per_million":0.15,"output_per_million":0.60,"cache_read_per_million":0.003,` +
		`"windows":[{"weekdays":["mon","fri"],"from":"01:00","to":"04:00",` +
		`"input_per_million":0.3,"output_per_million":1.2,"cache_read_per_million":0.006}]}}}}`)

	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	entry, ok := cfg.Pricing.Prices["deepseek/deepseek-flash"]
	if !ok {
		t.Fatalf("prices = %v, want the route", cfg.Pricing.Prices)
	}
	if entry.InputPerMillion != 0.15 {
		t.Errorf("the entry's own input rate = %v, want 0.15: a window must not replace it", entry.InputPerMillion)
	}
	if len(entry.Windows) != 1 {
		t.Fatalf("windows = %d, want 1", len(entry.Windows))
	}
	window := entry.Windows[0]
	if got, want := strings.Join(window.Weekdays, ","), "mon,fri"; got != want {
		t.Errorf("weekdays = %q, want %q", got, want)
	}
	if window.From != "01:00" || window.To != "04:00" {
		t.Errorf("window = %q-%q, want 01:00-04:00", window.From, window.To)
	}
	if window.InputPerMillion != 0.3 || window.OutputPerMillion != 1.2 || window.CacheReadPerMillion != 0.006 {
		t.Errorf("window rates = %v/%v/%v, want 0.3/1.2/0.006",
			window.InputPerMillion, window.OutputPerMillion, window.CacheReadPerMillion)
	}
}

// TestMergeKeepsOrReplacesWindowsWithTheirEntry pins the layering rule: a later
// layer naming the key replaces the whole entry, windows included, and a key it
// does not name keeps the schedule it had. Merging the rates but not the schedule
// would leave a key priced by one layer's hours and another layer's numbers.
func TestMergeKeepsOrReplacesWindowsWithTheirEntry(t *testing.T) {
	base := merge(DefaultConfig(), Config{Pricing: &PricingConfig{
		Currency: "USD",
		Prices: map[string]PriceEntry{
			"deepseek/*":   {InputPerMillion: 0.15, Windows: []PriceWindowEntry{{From: "01:00", To: "04:00"}}},
			"openrouter/*": {InputPerMillion: 3, Windows: []PriceWindowEntry{{From: "09:00", To: "17:00"}}},
		},
	}})

	got := merge(base, Config{Pricing: &PricingConfig{
		Prices: map[string]PriceEntry{
			"deepseek/*": {InputPerMillion: 0.2, Windows: []PriceWindowEntry{{From: "06:00", To: "10:00"}}},
		},
	}})

	deepseek := got.Pricing.Prices["deepseek/*"]
	if deepseek.InputPerMillion != 0.2 {
		t.Errorf("deepseek/* input = %v, want the later rate 0.2", deepseek.InputPerMillion)
	}
	if len(deepseek.Windows) != 1 || deepseek.Windows[0].From != "06:00" {
		t.Errorf("deepseek/* windows = %+v, want only the later layer's 06:00-10:00", deepseek.Windows)
	}
	if openrouter := got.Pricing.Prices["openrouter/*"]; len(openrouter.Windows) != 1 || openrouter.Windows[0].From != "09:00" {
		t.Errorf("openrouter/* windows = %+v, want the earlier layer's schedule kept", openrouter.Windows)
	}
}

// TestAPriceWindowRepeatsTheEntrysLanes is the structural half of the rule the
// agent enforces at runtime. A window that cannot spell a lane would leave it at
// zero inside its hours, which the agent refuses — but only after the lane is
// missing from the schema, where the message would be about the user's file while
// the defect is here. So the two spellings are held to the same five keys.
func TestAPriceWindowRepeatsTheEntrysLanes(t *testing.T) {
	entry := jsonKeys(PriceEntry{})
	window := jsonKeys(PriceWindowEntry{})

	// "windows" is the window list itself, which a window does not have.
	for key := range entry {
		if key == "windows" {
			continue
		}
		if !window[key] {
			t.Errorf("PriceEntry declares %q and PriceWindowEntry does not: a window could not repeat it", key)
		}
	}
	for key := range window {
		switch key {
		case "weekdays", "from", "to":
			continue
		}
		if !entry[key] {
			t.Errorf("PriceWindowEntry declares %q and PriceEntry does not", key)
		}
	}
	if len(entry) != 6 || len(window) != 8 {
		t.Errorf("entry keys = %d, window keys = %d; want 6 and 8 (this test counts them so an added field is seen)",
			len(entry), len(window))
	}
}

// jsonKeys is the set of JSON names a struct declares.
func jsonKeys(value any) map[string]bool {
	keys := map[string]bool{}
	typ := reflect.TypeOf(value)
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		keys[name] = true
	}
	return keys
}
