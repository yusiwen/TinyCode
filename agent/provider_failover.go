package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/yusiwen/tinycode/tlog"
	"github.com/yusiwen/tinycode/types"
)

// FailoverProvider is one route with the fallbacks its configuration declares.
//
// It exists because a transient failure is not always about the request: a route
// can be rate-limited, down, or unreachable while another configured route would
// answer it. The policy is deliberately narrow, and shares A1's classification:
//
//   - the primary has already spent its own retry budget (each provider retries
//     internally), so a failover happens only after the same route gave up;
//   - only a failure Retry() could not fix on the same route is followed by
//     another route, so a rejected key or a malformed request still surfaces
//     instead of silently moving the bill;
//   - nothing may have been streamed to the caller: an answer already drawn
//     cannot be taken back, and drawing a second one is worse than the error.
//
// A fallback is asked for its own configured model. Model names are
// vendor-specific, so carrying the primary's override across would usually ask
// the wrong thing.
type FailoverProvider struct {
	primary   LLMProvider
	fallbacks []LLMProvider
}

// NewFailoverProvider pairs a route with the routes to try after it, in order.
func NewFailoverProvider(primary LLMProvider, fallbacks []LLMProvider) *FailoverProvider {
	return &FailoverProvider{primary: primary, fallbacks: fallbacks}
}

// Fallbacks returns the chain this route fails over to, in the order it was
// flattened at startup. It is the same slice the call walks; it exists so a
// caller — a picker, a test — can show or assert the chain without sending a
// request.
func (f *FailoverProvider) Fallbacks() []LLMProvider {
	if f == nil {
		return nil
	}
	return f.fallbacks
}

// Chat tries the primary, then each fallback while the failure is one another
// route could survive and nothing has reached the caller.
func (f *FailoverProvider) Chat(ctx context.Context, req types.ChatRequest) (*types.ChatResponse, error) {
	if f == nil || f.primary == nil {
		return nil, fmt.Errorf("failover: no primary provider")
	}
	// One tracker for the whole chain: it records whether *any* attempt handed
	// text to the caller, which is the condition that forbids another attempt.
	tracker := trackedCallbacks(req.StreamCallbacks)
	attempt := req
	attempt.StreamCallbacks = tracker.callbacks()

	resp, err := f.primary.Chat(ctx, attempt)
	if err == nil {
		return resp, nil
	}
	if tracker.deliveredSomething() || !retryableError(ctx, err) {
		return nil, err
	}

	from := providerRoute(f.primary)
	chain := []string{from}
	lastErr := err
	for _, fallback := range f.fallbacks {
		if fallback == nil || ctx.Err() != nil {
			break
		}
		to := providerRoute(fallback)
		chain = append(chain, to)
		tlog.Warn("llm.failover", "from", from, "to", to,
			"reason", failureKind(lastErr), "error", lastErr.Error())
		notifyFailover(attempt.StreamCallbacks, from, to, lastErr)
		attempt.Model = "" // the fallback's own configured model
		resp, err = fallback.Chat(ctx, attempt)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if tracker.deliveredSomething() {
			return nil, err
		}
		if !retryableError(ctx, err) {
			return nil, fmt.Errorf("route %q: %w", to, err)
		}
		from = to
	}
	return nil, &failoverError{chain: chain, last: lastErr}
}

// Name names the backend that was tried first. The route's own configuration name
// lives on ProviderRecord, not here.
func (f *FailoverProvider) Name() string {
	if f == nil || f.primary == nil {
		return "none"
	}
	return f.primary.Name()
}

// Route is the configuration name of the primary route: the route a run's record
// names as its plan, even when a call inside it was served by a fallback.
func (f *FailoverProvider) Route() string {
	if f == nil || f.primary == nil {
		return ""
	}
	if info, ok := f.primary.(RouteInfo); ok {
		return info.Route()
	}
	return ""
}

// DefaultModel is the primary route's model.
func (f *FailoverProvider) DefaultModel() string {
	if f == nil || f.primary == nil {
		return ""
	}
	if info, ok := f.primary.(RouteInfo); ok {
		return info.DefaultModel()
	}
	return ""
}

// CostCurrency is the primary route's billing unit.
func (f *FailoverProvider) CostCurrency() string {
	if f == nil || f.primary == nil {
		return ""
	}
	if info, ok := f.primary.(RouteInfo); ok {
		return info.CostCurrency()
	}
	return ""
}

// failoverError reports every route that was tried, because "deepseek failed"
// when openrouter also failed is a different fact from "all of them failed".
type failoverError struct {
	chain []string
	last  error
}

func (e *failoverError) Error() string {
	return fmt.Sprintf("all routes failed (%s): %v", strings.Join(e.chain, ", "), e.last)
}

// Unwrap keeps errors.Is/As working for the error that ended the chain.
func (e *failoverError) Unwrap() error { return e.last }

// providerRoute names a provider for a message: its configured route when it can
// say one, otherwise its own display name. A message is better than a blank.
func providerRoute(p LLMProvider) string {
	if p == nil {
		return "none"
	}
	if info, ok := p.(RouteInfo); ok && info.Route() != "" {
		return info.Route()
	}
	return p.Name()
}

// failureKind classifies an error for the failover event and the log, so an
// analysis can say what kind of failure moved a call without replaying the
// message. It is a description, not a policy: the decision is retryableError's.
func failureKind(err error) string {
	if err == nil {
		return ""
	}
	var se *statusError
	if errors.As(err, &se) {
		switch {
		case se.status == http.StatusTooManyRequests:
			return "rate_limit"
		case se.status == http.StatusRequestTimeout:
			return "timeout"
		case se.status >= 500:
			return "server_error"
		default:
			return "status"
		}
	}
	if isSilentStream(err) {
		return "timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if isProtocolError(err) {
		return "protocol"
	}
	return "network"
}

// notifyFailover announces a route change before the next request, so a caller
// can show it. Like notifyRetry, the policy stays here and the caller only
// renders the event.
func notifyFailover(cb *types.StreamCallbacks, from, to string, err error) {
	if cb != nil && cb.OnFailover != nil {
		cb.OnFailover(from, to, err)
	}
}
