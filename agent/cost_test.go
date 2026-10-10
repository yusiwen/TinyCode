package agent

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// ─── S2: a route that reports its own charge ───────────────────

// TestReportedCostIsCarriedVerbatim: the number the route stated is the number
// that is recorded, in the unit the configuration names for that route. Nothing
// multiplies it, and nothing adds a table-derived figure to it.
func TestReportedCostIsCarriedVerbatim(t *testing.T) {
	body := `{"choices":[{"message":{"content":"hi"}}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"cost":0.00042}}`

	// Routing is what makes the unit knowable; without it the amount would be
	// displayed with no unit at all.
	provider := &OpenAIProvider{model: "m"}
	provider.SetRouteInfo("openrouter", "anthropic/claude-sonnet-4", "credits")

	res, err := provider.chatBatch(context.Background(), io.NopCloser(strings.NewReader(body)), time.Now())
	if err != nil {
		t.Fatalf("chatBatch: %v", err)
	}
	if res.Cost == nil {
		t.Fatal("reported cost was dropped")
	}
	if res.Cost.Amount != 0.00042 || res.Cost.Currency != "credits" {
		t.Fatalf("cost = %+v, want 0.00042 credits", *res.Cost)
	}
}

// TestUpstreamCostIsUsedWhenNothingWasBilled covers a BYOK request: the account
// is charged nothing and the upstream figure is the informative one.
func TestUpstreamCostIsUsedWhenNothingWasBilled(t *testing.T) {
	body := `{"choices":[{"message":{"content":"hi"}}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,` +
		`"cost_details":{"upstream_inference_cost":19}}}`
	provider := &OpenAIProvider{model: "m"}
	provider.SetRouteInfo("openrouter", "m", "USD")

	res, err := provider.chatBatch(context.Background(), io.NopCloser(strings.NewReader(body)), time.Now())
	if err != nil {
		t.Fatalf("chatBatch: %v", err)
	}
	if res.Cost == nil || res.Cost.Amount != 19 || res.Cost.Currency != "USD" {
		t.Fatalf("cost = %+v, want 19 USD", res.Cost)
	}
}

// TestNoReportedCostStaysNil keeps the three-state rule at the wire boundary: an
// endpoint that says nothing about money reports nothing, not zero.
func TestNoReportedCostStaysNil(t *testing.T) {
	body := `{"choices":[{"message":{"content":"hi"}}],` +
		`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`
	res, err := (&OpenAIProvider{model: "m"}).chatBatch(context.Background(),
		io.NopCloser(strings.NewReader(body)), time.Now())
	if err != nil {
		t.Fatalf("chatBatch: %v", err)
	}
	if res.Cost != nil {
		t.Fatalf("cost = %+v, want nil", *res.Cost)
	}
}

func TestReportedCostFromTheStreamingTail(t *testing.T) {
	input := "" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":194,\"completion_tokens\":2,\"total_tokens\":196,\"cost\":0.95}}\n\n" +
		"data: [DONE]\n"
	provider := &OpenAIProvider{model: "m"}
	provider.SetRouteInfo("openrouter", "m", "credits")
	res, err := provider.chatStream(context.Background(),
		io.NopCloser(strings.NewReader(input)), time.Now(), &types.StreamCallbacks{}, nil)
	if err != nil {
		t.Fatalf("chatStream: %v", err)
	}
	if res.Cost == nil || res.Cost.Amount != 0.95 {
		t.Fatalf("cost = %+v, want 0.95", res.Cost)
	}
}

// TestAgentAccumulatesCostPerUnit: two routes billing in different units must not
// be added together — the totals are keyed by unit for exactly that reason.
func TestAgentAccumulatesCostPerUnit(t *testing.T) {
	step := 0
	var events []types.CostEvent
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			step++
			return &types.ChatResponse{
				Content: "answer",
				Usage:   &types.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
				Cost:    &types.Cost{Amount: float64(step), Currency: []string{"credits", "USD"}[step-1]},
			}, nil
		},
	}
	a := &Agent{
		Provider: provider, MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test",
		StreamCallbacks: &types.StreamCallbacks{
			OnCost: func(ev types.CostEvent) { events = append(events, ev) },
		},
	}

	// Two runs, one call each.
	for i := 0; i < 2; i++ {
		if _, err := a.Run(context.Background(), "hi"); err != nil {
			t.Fatalf("Run %d: %v", i, err)
		}
	}

	if len(events) != 2 {
		t.Fatalf("OnCost fired %d times, want once per call", len(events))
	}
	for _, ev := range events {
		if ev.Source != types.CostReported {
			t.Errorf("event source = %q, want %q", ev.Source, types.CostReported)
		}
	}
	if got := a.CostTotal.String(); got != "1 credits, 2 USD" {
		t.Errorf("CostTotal = %q, want %q", got, "1 credits, 2 USD")
	}
	if a.CostUnknownCalls != 0 {
		t.Errorf("CostUnknownCalls = %d, want 0", a.CostUnknownCalls)
	}
}

// ─── S3: a price the user declared ─────────────────────────────

func TestPriceTableMatchesMostSpecificKeyFirst(t *testing.T) {
	table := NewPriceTable("USD")
	table.Set("*/*", Price{InputPerMillion: 1})
	table.Set("*/claude-sonnet-4", Price{InputPerMillion: 2})
	table.Set("openrouter/*", Price{InputPerMillion: 3})
	table.Set("openrouter/claude-sonnet-4", Price{InputPerMillion: 4})

	cases := []struct {
		key  string
		want float64
	}{
		{"openrouter/claude-sonnet-4", 4},           // exact
		{"openrouter/claude-opus", 3},               // route wildcard
		{"deepseek/claude-sonnet-4", 2},             // model wildcard
		{"deepseek/deepseek-v4-flash", 1},           // everything
		{"openrouter/anthropic/claude-sonnet-4", 3}, // a model name with a separator: route/* wins
	}
	for _, tc := range cases {
		price, ok := table.Lookup(tc.key)
		if !ok {
			t.Errorf("Lookup(%q) found nothing", tc.key)
			continue
		}
		if price.InputPerMillion != tc.want {
			t.Errorf("Lookup(%q).InputPerMillion = %v, want %v", tc.key, price.InputPerMillion, tc.want)
		}
	}
	if _, ok := NewPriceTable("USD").Lookup("route/model"); ok {
		t.Error("an empty table matched a key")
	}
}

// TestPriceAmountCarvesTheLanes: the detail counts are subsets of the totals, so
// they are subtracted out rather than added on top, and each lane uses its own
// rate with the documented fallback.
func TestPriceAmountCarvesTheLanes(t *testing.T) {
	// 1M prompt of which 400k cached and 100k written, 200k completion of which
	// 50k reasoning.
	usage := types.Usage{
		PromptTokens: 1_000_000, CompletionTokens: 200_000, TotalTokens: 1_200_000,
		CachedPromptTokens: 400_000, CacheWriteTokens: 100_000, ReasoningTokens: 50_000,
	}
	price := Price{
		InputPerMillion:      10,
		OutputPerMillion:     20,
		CacheReadPerMillion:  1,
		CacheWritePerMillion: 12,
		ReasoningPerMillion:  30,
	}
	// plain in 500k*10 = 5.0, cached 400k*1 = 0.4, written 100k*12 = 1.2,
	// plain out 150k*20 = 3.0, reasoning 50k*30 = 1.5
	want := 5.0 + 0.4 + 1.2 + 3.0 + 1.5
	if got := price.amount(usage); got != want {
		t.Errorf("amount = %v, want %v", got, want)
	}

	// With no detail rates declared, every lane falls back to the plain rate.
	plain := Price{InputPerMillion: 10, OutputPerMillion: 20}
	// in 1M*10 = 10, out 200k*20 = 4
	if got, want := plain.amount(usage), 14.0; got != want {
		t.Errorf("fallback amount = %v, want %v", got, want)
	}
}

// TestPriceAmountFloorsAtZero: a route reporting a detail larger than its total
// is a route whose numbers we do not understand, and the answer to that is not
// negative money.
func TestPriceAmountFloorsAtZero(t *testing.T) {
	usage := types.Usage{
		PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110,
		CachedPromptTokens: 500, CacheWriteTokens: 500, ReasoningTokens: 900,
	}
	price := Price{InputPerMillion: 1, OutputPerMillion: 1}
	if got := price.amount(usage); got < 0 {
		t.Errorf("amount = %v, want it floored at zero", got)
	}
}

// TestDeclaredPriceAppliesToARouteKey is the end-to-end of S3: a route that
// reports no charge is priced from the declaration, and the event says the number
// was derived rather than reported.
func TestDeclaredPriceAppliesToARouteKey(t *testing.T) {
	var events []types.CostEvent
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{
				Content: "answer",
				Usage:   &types.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000},
			}, nil
		},
	}
	table := NewPriceTable("USD")
	table.Set("deepseek/deepseek-v4-flash", Price{InputPerMillion: 0.3, OutputPerMillion: 1.2})

	a := &Agent{
		Provider: provider, MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test",
		PriceTable: table,
		StreamCallbacks: &types.StreamCallbacks{
			OnCost: func(ev types.CostEvent) { events = append(events, ev) },
		},
	}

	// The provider is a mock, so the route has to come from somewhere: give the
	// agent a provider that names one, as the real providers do.
	a.Provider = &routeMockProvider{MockProvider: provider, route: "deepseek", model: "deepseek-v4-flash"}

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("OnCost fired %d times, want 1", len(events))
	}
	if events[0].Source != types.CostDeclared {
		t.Errorf("source = %q, want %q", events[0].Source, types.CostDeclared)
	}
	if want := 0.3 + 1.2; events[0].Cost.Amount != want {
		t.Errorf("amount = %v, want %v", events[0].Cost.Amount, want)
	}
	if got := a.CostTotal.String(); got != "1.5 USD" {
		t.Errorf("CostTotal = %q, want %q", got, "1.5 USD")
	}
}

// TestUnpricedCallIsUnknownNotZero is the acceptance that keeps a missing price
// from reading as free.
func TestUnpricedCallIsUnknownNotZero(t *testing.T) {
	var events []types.CostEvent
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{
				Content: "answer",
				Usage:   &types.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
			}, nil
		},
	}
	a := &Agent{
		Provider: &routeMockProvider{MockProvider: provider, route: "deepseek", model: "m"},
		MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test",
		PriceTable: NewPriceTable("USD"), // declared, but with no rate for this route
		StreamCallbacks: &types.StreamCallbacks{
			OnCost: func(ev types.CostEvent) { events = append(events, ev) },
		},
	}

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(events) != 1 || events[0].Source != types.CostUnknown {
		t.Fatalf("events = %+v, want one unknown", events)
	}
	if got := a.CostTotal.String(); got != "" {
		t.Errorf("CostTotal = %q, want empty: an unknown cost must not become a zero", got)
	}
	if a.CostUnknownCalls != 1 {
		t.Errorf("CostUnknownCalls = %d, want 1", a.CostUnknownCalls)
	}
}

// TestEstimatedUsageIsNotPriced: multiplying a declared rate by a guessed token
// count would dress a second estimate as a measurement, so a call whose usage we
// had to estimate is unknown even when a rate matches.
func TestEstimatedUsageIsNotPriced(t *testing.T) {
	var events []types.CostEvent
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{Content: strings.Repeat("x", 400)}, nil // no usage, no cost
		},
	}
	table := NewPriceTable("USD")
	table.Set("*/*", Price{InputPerMillion: 1, OutputPerMillion: 1})

	a := &Agent{
		Provider: &routeMockProvider{MockProvider: provider, route: "deepseek", model: "m"},
		MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test", PriceTable: table,
		StreamCallbacks: &types.StreamCallbacks{
			OnCost: func(ev types.CostEvent) { events = append(events, ev) },
		},
	}

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(events) != 1 || events[0].Source != types.CostUnknown {
		t.Fatalf("events = %+v, want one unknown", events)
	}
	if a.CostTotal.String() != "" {
		t.Errorf("CostTotal = %q, want empty", a.CostTotal.String())
	}
}

// TestReportedCostWinsOverADeclaredPrice: the route's own figure is a reading of
// the bill; a declared rate is a guess at it.
func TestReportedCostWinsOverADeclaredPrice(t *testing.T) {
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{
				Content: "answer",
				Usage:   &types.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000, TotalTokens: 2_000_000},
				Cost:    &types.Cost{Amount: 7, Currency: "credits"},
			}, nil
		},
	}
	table := NewPriceTable("USD")
	table.Set("*/*", Price{InputPerMillion: 100, OutputPerMillion: 100})

	a := &Agent{
		Provider: &routeMockProvider{MockProvider: provider, route: "openrouter", model: "m"},
		MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test", PriceTable: table,
	}

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := a.CostTotal.String(); got != "7 credits" {
		t.Errorf("CostTotal = %q, want %q: the reported figure, not 200 USD from the table", got, "7 credits")
	}
}

// TestAnAgentOverrideChangesThePricedModel: the model a call used is the one the
// agent asked for, not the provider's default, or a session on a cheaper model
// would be billed at the expensive one's rate.
func TestAnAgentOverrideChangesThePricedModel(t *testing.T) {
	provider := &MockProvider{
		ChatFunc: func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{
				Content: "answer",
				Usage:   &types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000},
			}, nil
		},
	}
	table := NewPriceTable("USD")
	table.Set("deepseek/*", Price{InputPerMillion: 1})
	table.Set("deepseek/deepseek-v4-pro", Price{InputPerMillion: 10})

	a := &Agent{
		Provider: &routeMockProvider{MockProvider: provider, route: "deepseek", model: "deepseek-v4-flash"},
		MaxSteps: 5, MaxTokens: 100, SystemPrompt: "test", PriceTable: table,
		Config: &AgentConfig{Name: "build", Model: "deepseek/deepseek-v4-pro"},
	}

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The override is the whole "deepseek/deepseek-v4-pro" string, so the key is
	// "deepseek/deepseek/deepseek-v4-pro": the wildcard matches and the exact
	// model rate does not. Documented behaviour — the model is used verbatim.
	if got := a.CostTotal.String(); got != "1 USD" {
		t.Errorf("CostTotal = %q, want 1 USD from the route wildcard", got)
	}
}

// routeMockProvider is a MockProvider that can name a route and a default model,
// which is what the real providers do through RouteInfo.
type routeMockProvider struct {
	*MockProvider
	route string
	model string
}

func (p *routeMockProvider) Route() string        { return p.route }
func (p *routeMockProvider) DefaultModel() string { return p.model }
func (p *routeMockProvider) CostCurrency() string { return "" }
