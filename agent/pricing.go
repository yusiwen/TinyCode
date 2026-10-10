package agent

import (
	"strings"

	"github.com/yusiwen/tinycode/types"
)

// Price is what a route charges for a million tokens, per billing lane.
//
// A zero rate is "not declared" rather than "free", and the fallbacks in Amount
// say what it falls back to: a route that charges the same for cached and
// uncached input declares one input rate, and one that charges nothing for
// anything is not expressible by accident.
type Price struct {
	InputPerMillion      float64
	OutputPerMillion     float64
	CacheReadPerMillion  float64 // 0 → the input rate
	CacheWritePerMillion float64 // 0 → the input rate
	ReasoningPerMillion  float64 // 0 → the output rate
}

// PriceTable is a user's declaration of what their routes charge, keyed by
// "<route>/<model>".
//
// The route is part of the key because a price belongs to a billing endpoint,
// not to a model name: the same model costs differently through a reseller, a
// gateway and the vendor's own API. A route that reports the charge itself never
// consults this table.
//
// A key may use "*" for the route, for the model, or for both, so one rate can
// cover a whole route or a whole family. An exact key always wins, then
// "route/*", then "*/model", then "*/*" — the order is fixed so two entries can
// never both claim a call.
type PriceTable struct {
	// Currency names the unit the declared rates are in, e.g. "USD". It is the
	// user's invoice unit, which is not necessarily the unit a route reports its
	// own charge in; the two are accumulated apart rather than added.
	Currency string
	prices   map[string]Price
}

// NewPriceTable returns an empty table. A nil table prices nothing, which is
// reported as an unknown cost rather than as zero.
func NewPriceTable(currency string) *PriceTable {
	return &PriceTable{Currency: currency, prices: map[string]Price{}}
}

// Set declares the rate for one key. An empty key is ignored: a declaration that
// cannot match anything is a typo, not a rate for everything.
func (t *PriceTable) Set(key string, price Price) {
	if t == nil || strings.TrimSpace(key) == "" {
		return
	}
	if t.prices == nil {
		t.prices = map[string]Price{}
	}
	t.prices[key] = price
}

// Len is the number of declared keys, for a caller that has to report whether a
// table is empty.
func (t *PriceTable) Len() int {
	if t == nil {
		return 0
	}
	return len(t.prices)
}

// Lookup returns the rate that covers a key, most specific first.
func (t *PriceTable) Lookup(key string) (Price, bool) {
	if t == nil {
		return Price{}, false
	}
	route, model, ok := splitPriceKey(key)
	if !ok {
		return Price{}, false
	}
	for _, candidate := range []string{
		route + "/" + model,
		route + "/*",
		"*/" + model,
		"*/*",
	} {
		if price, found := t.prices[candidate]; found {
			return price, true
		}
	}
	return Price{}, false
}

// Amount prices one usage record, or reports that no declared rate covers the
// call. The result is an estimate of the bill computed from rates the user
// typed, which is why it comes back with its source attached by the caller.
func (t *PriceTable) Amount(key string, usage types.Usage) (types.Cost, bool) {
	price, ok := t.Lookup(key)
	if !ok {
		return types.Cost{}, false
	}
	return types.Cost{Amount: price.amount(usage), Currency: t.Currency}, true
}

// amount applies the lane rates to one usage record.
//
// The detail fields are subsets of the counts beside them (see types.Usage), so
// the lanes are carved out of the totals rather than added on top: cached and
// written tokens come out of the prompt, reasoning tokens out of the completion.
// Each subtraction is floored at zero, because a route that reports a detail
// larger than its total is a route whose numbers we do not understand, and
// negative money is not a useful way to say so.
func (p Price) amount(u types.Usage) float64 {
	const million = 1_000_000

	writeRate := p.CacheWritePerMillion
	if writeRate == 0 {
		writeRate = p.InputPerMillion
	}
	readRate := p.CacheReadPerMillion
	if readRate == 0 {
		readRate = p.InputPerMillion
	}
	reasoningRate := p.ReasoningPerMillion
	if reasoningRate == 0 {
		reasoningRate = p.OutputPerMillion
	}

	written := min(max(u.CacheWriteTokens, 0), u.PromptTokens)
	cached := min(max(u.CachedPromptTokens, 0), u.PromptTokens-written)
	plainInput := max(u.PromptTokens-written-cached, 0)

	reasoning := min(max(u.ReasoningTokens, 0), u.CompletionTokens)
	plainOutput := max(u.CompletionTokens-reasoning, 0)

	total := float64(plainInput)*p.InputPerMillion +
		float64(cached)*readRate +
		float64(written)*writeRate +
		float64(plainOutput)*p.OutputPerMillion +
		float64(reasoning)*reasoningRate
	return total / million
}

// splitPriceKey splits "route/model" on the first separator, because a model
// name may contain one: an OpenRouter model is itself "vendor/model".
func splitPriceKey(key string) (route, model string, ok bool) {
	route, model, ok = strings.Cut(key, "/")
	if !ok || route == "" || model == "" {
		return "", "", false
	}
	return route, model, true
}

// priceKey names the route and model a call is priced against.
//
// The model is whatever the request actually used: an agent-level override when
// it has one, otherwise the provider's own default. It comes from the provider
// at call time rather than from a value captured when the agent was built,
// because the route can be switched mid-session.
func (a *Agent) priceKey() (string, bool) {
	info, ok := a.Provider.(RouteInfo)
	if !ok {
		return "", false
	}
	route, model := info.Route(), info.DefaultModel()
	if override := a.getModel(); override != "" {
		model = override
	}
	if route == "" || model == "" {
		return "", false
	}
	return route + "/" + model, true
}
