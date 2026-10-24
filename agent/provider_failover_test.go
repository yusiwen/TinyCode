package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yusiwen/tinycode/types"
)

// scriptedRoute is a provider that records its calls and answers from a script.
// It carries a route and a model so the failover wrapper can name it and the
// agent can price the call that it served.
type scriptedRoute struct {
	route, model string
	name         string
	calls        int
	seenModel    string
	chat         func(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error)
}

func (p *scriptedRoute) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	p.calls++
	p.seenModel = req.Model
	if p.chat != nil {
		return p.chat(ctx, req)
	}
	return &types.ChatResponse{Content: p.route + "-answer"}, nil
}

func (p *scriptedRoute) Name() string         { return p.name }
func (p *scriptedRoute) Route() string        { return p.route }
func (p *scriptedRoute) DefaultModel() string { return p.model }
func (p *scriptedRoute) CostCurrency() string { return "" }

// TestFailoverMovesATransientFailureToTheNextRoute is S2's acceptance: after the
// primary fails transiently, one request goes to the fallback, the answer is the
// fallback's, the caller is told once, and the fallback is asked for its own model
// rather than the primary's override.
func TestFailoverMovesATransientFailureToTheNextRoute(t *testing.T) {
	primary := &scriptedRoute{
		route: "deepseek", model: "deepseek-flash", name: "openai/deepseek-flash",
		chat: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
			return nil, &statusError{status: 429, body: "slow down"}
		},
	}
	fallback := &scriptedRoute{route: "openrouter", model: "claude", name: "openai/claude"}
	provider := NewFailoverProvider(primary, []LLMProvider{fallback})

	var events []string
	var reasons []string
	resp, err := provider.Chat(context.Background(), types.ChatRequest{
		Model: "pinned-model",
		StreamCallbacks: &types.StreamCallbacks{
			OnFailover: func(from, to string, err error) {
				events = append(events, from+"->"+to)
				reasons = append(reasons, failureKind(err))
			},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "openrouter-answer" {
		t.Errorf("answer = %q, want the fallback's", resp.Content)
	}
	if primary.calls != 1 || fallback.calls != 1 {
		t.Errorf("calls = primary:%d fallback:%d, want one each", primary.calls, fallback.calls)
	}
	if primary.seenModel != "pinned-model" {
		t.Errorf("primary saw model %q, want the request override", primary.seenModel)
	}
	if fallback.seenModel != "" {
		t.Errorf("fallback saw model %q, want its own configured model", fallback.seenModel)
	}
	if len(events) != 1 || events[0] != "deepseek->openrouter" {
		t.Errorf("failover events = %v, want one deepseek->openrouter", events)
	}
	if len(reasons) != 1 || reasons[0] != "rate_limit" {
		t.Errorf("failure kinds = %v, want [rate_limit]", reasons)
	}
}

// TestNoFailoverAfterTextWasDelivered: an attempt the caller has already seen may
// not be replayed on another route, or the answer would be drawn twice.
func TestNoFailoverAfterTextWasDelivered(t *testing.T) {
	primary := &scriptedRoute{
		route: "deepseek", model: "m", name: "openai/m",
		chat: func(_ context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
			req.StreamCallbacks.OnTextDelta("partial answer")
			return nil, &statusError{status: 429, body: "slow down"}
		},
	}
	fallback := &scriptedRoute{route: "openrouter", model: "m2", name: "openai/m2"}
	provider := NewFailoverProvider(primary, []LLMProvider{fallback})

	_, err := provider.Chat(context.Background(), types.ChatRequest{
		StreamCallbacks: &types.StreamCallbacks{OnTextDelta: func(string) {}},
	})
	if err == nil {
		t.Fatal("an error after a delivered delta must be returned, not hidden")
	}
	if fallback.calls != 0 {
		t.Errorf("fallback calls = %d, want 0 after a delivered delta", fallback.calls)
	}
}

// TestNoFailoverOnANonTransientError: a rejected key or a malformed request is a
// configuration error the user has to see, not a reason to spend on another route.
func TestNoFailoverOnANonTransientError(t *testing.T) {
	for _, status := range []int{400, 401, 404} {
		primary := &scriptedRoute{
			route: "deepseek", model: "m", name: "openai/m",
			chat: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
				return nil, &statusError{status: status, body: "no"}
			},
		}
		fallback := &scriptedRoute{route: "openrouter", model: "m2", name: "openai/m2"}
		_, err := NewFailoverProvider(primary, []LLMProvider{fallback}).Chat(context.Background(), types.ChatRequest{})
		var se *statusError
		if !errors.As(err, &se) || se.status != status {
			t.Errorf("status %d: error = %v, want the original failure", status, err)
		}
		if fallback.calls != 0 {
			t.Errorf("status %d: fallback calls = %d, want 0", status, fallback.calls)
		}
	}
}

// TestNoFailoverAfterCancellation: the caller asked to stop, and another request
// would ignore them.
func TestNoFailoverAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	primary := &scriptedRoute{
		route: "deepseek", model: "m", name: "openai/m",
		chat: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
			cancel()
			return nil, context.Canceled
		},
	}
	fallback := &scriptedRoute{route: "openrouter", model: "m2", name: "openai/m2"}
	_, err := NewFailoverProvider(primary, []LLMProvider{fallback}).Chat(ctx, types.ChatRequest{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want the cancellation", err)
	}
	if fallback.calls != 0 {
		t.Errorf("fallback calls = %d, want 0 after a cancellation", fallback.calls)
	}
}

// TestFailoverTriesRoutesInOrderAndStopsAtTheFirstSuccess.
func TestFailoverTriesRoutesInOrderAndStopsAtTheFirstSuccess(t *testing.T) {
	primary := &scriptedRoute{
		route: "a", model: "m", name: "openai/a",
		chat: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
			return nil, &statusError{status: 503, body: "down"}
		},
	}
	second := &scriptedRoute{
		route: "b", model: "m", name: "openai/b",
		chat: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
			return nil, &statusError{status: 500, body: "also down"}
		},
	}
	third := &scriptedRoute{route: "c", model: "m", name: "openai/c"}
	provider := NewFailoverProvider(primary, []LLMProvider{second, third})

	if _, err := provider.Chat(context.Background(), types.ChatRequest{}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if primary.calls != 1 || second.calls != 1 || third.calls != 1 {
		t.Errorf("calls = %d/%d/%d, want one per route tried in order", primary.calls, second.calls, third.calls)
	}
}

// TestFailoverErrorNamesEveryRouteTried: "all routes failed" has to say which, or
// the failure reads like one route's.
func TestFailoverErrorNamesEveryRouteTried(t *testing.T) {
	primary := &scriptedRoute{
		route: "a", model: "m", name: "openai/a",
		chat: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
			return nil, &statusError{status: 429, body: "slow"}
		},
	}
	fallback := &scriptedRoute{
		route: "b", model: "m", name: "openai/b",
		chat: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
			return nil, &statusError{status: 503, body: "down"}
		},
	}
	_, err := NewFailoverProvider(primary, []LLMProvider{fallback}).Chat(context.Background(), types.ChatRequest{})
	if err == nil {
		t.Fatal("expected an error when every route fails")
	}
	for _, want := range []string{"a", "b", "all routes failed"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
	var se *statusError
	if !errors.As(err, &se) || se.status != 503 {
		t.Errorf("errors.As = %v, want the last route's failure", se)
	}
}

// TestFailoverProviderKeepsThePrimarysIdentity: the wrapper is a route like any
// other to the planner, the price table and the record.
func TestFailoverProviderKeepsThePrimarysIdentity(t *testing.T) {
	primary := &scriptedRoute{route: "deepseek", model: "deepseek-flash", name: "openai/deepseek-flash"}
	provider := NewFailoverProvider(primary, []LLMProvider{&scriptedRoute{route: "b", name: "b"}})
	if provider.Name() != "openai/deepseek-flash" || provider.Route() != "deepseek" ||
		provider.DefaultModel() != "deepseek-flash" {
		t.Errorf("identity = %q/%q/%q, want the primary's", provider.Name(), provider.Route(), provider.DefaultModel())
	}
}

// TestFailedOverCallIsPricedByTheServingRoute: the route that answered pays, not
// the one that was planned (roadmap A5, S2).
func TestFailedOverCallIsPricedByTheServingRoute(t *testing.T) {
	primary := &scriptedRoute{
		route: "cheap", model: "cm", name: "openai/cheap",
		chat: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
			return nil, &statusError{status: 429, body: "slow"}
		},
	}
	fallback := &scriptedRoute{
		route: "pricey", model: "pm", name: "openai/pricey",
		chat: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{
				Content: "ok",
				Usage:   &types.Usage{PromptTokens: 1_000_000, TotalTokens: 1_000_000},
				Route:   "pricey",
				Model:   "pm",
			}, nil
		},
	}
	table := NewPriceTable("USD")
	table.Set("cheap/cm", Price{InputPerMillion: 1})
	table.Set("pricey/pm", Price{InputPerMillion: 2})

	var costs []types.CostEvent
	a := &Agent{
		Provider:   NewFailoverProvider(primary, []LLMProvider{fallback}),
		Config:     &AgentConfig{Name: "build"},
		MaxSteps:   2,
		MaxTokens:  16,
		PriceTable: table,
		StreamCallbacks: &types.StreamCallbacks{
			OnCost: func(ev types.CostEvent) { costs = append(costs, ev) },
		},
	}
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(costs) != 1 || costs[0].Cost.Amount != 2 {
		t.Fatalf("costs = %+v, want one call priced at the serving route's 2 USD", costs)
	}
	if got := a.CostTotal.String(); got != "2 USD" {
		t.Errorf("CostTotal = %q, want %q", got, "2 USD")
	}
}

// TestFailureKind pins the vocabulary an analysis reads from the event.
func TestFailureKind(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{&statusError{status: 429}, "rate_limit"},
		{&statusError{status: 503}, "server_error"},
		{&statusError{status: 408}, "timeout"},
		{&statusError{status: 400}, "status"},
		{errOpenAISilent, "timeout"},
		{errors.New("parse response: bad json"), "protocol"},
		{errors.New("dial tcp: connection refused"), "network"},
	}
	for _, tc := range tests {
		if got := failureKind(tc.err); got != tc.want {
			t.Errorf("failureKind(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

// TestAFailoverLeavesItsOwnEventAndTheServingRoute: the run's record names the
// planned route once, and every call that moved says where it went and why
// (roadmap A5, S3).
func TestAFailoverLeavesItsOwnEventAndTheServingRoute(t *testing.T) {
	dir := t.TempDir()
	primary := &scriptedRoute{
		route: "a", model: "am", name: "openai/a",
		chat: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
			return nil, &statusError{status: 429, body: "slow down"}
		},
	}
	fallback := &scriptedRoute{
		route: "b", model: "bm", name: "openai/b",
		chat: func(context.Context, types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{Content: "ok", Route: "b", Model: "bm"}, nil
		},
	}
	a := New(NewFailoverProvider(primary, []LLMProvider{fallback}))
	a.Trajectory = NewTrajectory(dir, TrajectoryCaptureMetadata)

	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	_, events := onlyRun(t, dir)
	var failovers, calls int
	for _, event := range events {
		switch event["type"] {
		case TrajectoryRouteFailover:
			failovers++
			if event["step"] != float64(0) || event["from"] != "a" || event["to"] != "b" ||
				event["reason"] != "rate_limit" {
				t.Errorf("route.failover = %v, want step 0 a→b rate_limit", event)
			}
		case TrajectoryLLMCall:
			calls++
			if event["route"] != "b" || event["model"] != "bm" {
				t.Errorf("llm.call route/model = %v/%v, want the serving route b/bm",
					event["route"], event["model"])
			}
		}
	}
	if failovers != 1 || calls != 1 {
		t.Errorf("failover events = %d, llm.call events = %d; want one each", failovers, calls)
	}
	if events[0]["type"] != TrajectoryRunStart ||
		events[0]["provider"] != "a" || events[0]["model"] != "am" {
		t.Errorf("run.start = %v, want the planned route a/am", events[0])
	}
}
