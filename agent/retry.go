package agent

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// Retry policy for one provider call.
//
// A transient failure — a rate limit, a model still loading, a connection reset
// before the response — used to end the step. These bounds measure a different
// thing from the request bounds beside them (openAIRequestTimeout,
// openAIIdleTimeout, ollamaIdleTimeout): those bound how long one attempt may
// take, these bound how many attempts are made and how long the pause between
// them grows.
//
// The defaults are what a person would want twice a day: three attempts total,
// half a second of pause growing to at most ten seconds. All of them are
// variables so a test can shrink the schedule to nothing and assert a counted
// request total instead of a wall-clock delay.
var (
	// retryMaxAttempts counts the first attempt: 3 means one initial call and up
	// to two retries.
	retryMaxAttempts = 3
	// retryBaseDelay is the first pause; later pauses double it.
	retryBaseDelay = 500 * time.Millisecond
	// retryMaxDelay caps one pause, however many attempts have passed and
	// whatever Retry-After asked for.
	retryMaxDelay = 10 * time.Second
)

// retryJitter is the source of the delay's randomness. It is a variable so a test
// can make the schedule exact; production keeps a full [0,1) draw per pause,
// which is what stops a fleet of clients that failed together from retrying
// together.
var retryJitter = rand.Float64

// statusError is an HTTP failure, carrying what the classification needs: the
// status, a bounded excerpt of the body for the message, and how long the
// endpoint asked the caller to wait.
type statusError struct {
	status     int
	body       string
	retryAfter time.Duration
}

func (e *statusError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("status %d", e.status)
	}
	return fmt.Sprintf("status %d: %s", e.status, e.body)
}

// Retryable reports whether a second attempt could plausibly succeed. A rate
// limit and a server-side failure can change on their own; the other 4xx cannot
// — a rejected key, a malformed request and a missing model will fail the same
// way however many times they are sent.
func (e *statusError) Retryable() bool {
	switch {
	case e.status == http.StatusRequestTimeout: // 408
		return true
	case e.status == http.StatusTooManyRequests: // 429
		return true
	case e.status >= 500: // 500, 502, 503, 504, ...
		return true
	default:
		return false
	}
}

// parseRetryAfter reads the delta-seconds form of Retry-After.
//
// The HTTP-date form is deliberately not parsed: a mis-parsed date would produce
// an arbitrary delay (a date a year out would pause the run for a year), and the
// endpoint that cares about the wait can be reached again by the next attempt
// instead. Returning zero means "no instruction", which leaves the schedule in
// charge.
func parseRetryAfter(header string) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	seconds, err := strconv.Atoi(header)
	if err != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// backoffDelay is the pause before the next attempt: exponential backoff with
// equal jitter, floored at what the endpoint asked for and capped at
// retryMaxDelay.
//
// attempt is the one that just failed (1 for the first). Equal jitter — half the
// window fixed, half random — keeps the growth an operator can reason about
// while still spreading clients that failed together.
func backoffDelay(attempt int, retryAfter time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	window := retryBaseDelay
	for i := 1; i < attempt; i++ {
		window *= 2
		if window >= retryMaxDelay {
			break
		}
	}
	if window > retryMaxDelay {
		window = retryMaxDelay
	}

	delay := window/2 + time.Duration(retryJitter()*float64(window/2))
	if retryAfter > delay {
		delay = retryAfter
	}
	if delay > retryMaxDelay {
		delay = retryMaxDelay
	}
	return delay
}

// retryAfterOf reports the wait an endpoint asked for, if the error carries one.
func retryAfterOf(err error) time.Duration {
	var se *statusError
	if errors.As(err, &se) {
		return se.retryAfter
	}
	return 0
}

// retryableError decides whether one attempt may be repeated.
//
// A context that has ended is never retried: the caller asked to stop, and
// spending another request on it would ignore them. A JSON or protocol error is
// never retried either — a body we cannot read will not become readable by
// asking again, and a retry would double the cost of a real bug.
func retryableError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var se *statusError
	if errors.As(err, &se) {
		return se.Retryable()
	}
	// The stream idle bound (issue #176) ends a read that produced nothing for
	// its whole window. That is exactly the silent endpoint a second attempt may
	// reach, and the delivery check keeps it honest for a call that had already
	// streamed something.
	if isSilentStream(err) {
		return true
	}
	// A transport error that produced no response at all — a refused connection,
	// a reset, a TLS handshake that timed out. It is reached through the client's
	// error return, so no status was available to classify.
	var se2 *statusError
	if !errors.As(err, &se2) && !isProtocolError(err) {
		return true
	}
	return false
}

// errStreamSilent is the cancel cause shared by the native backends added for
// roadmap A6. Each of them bounds the silence between streamed events; the
// message is deliberately vendor-neutral there, while the two older providers
// keep their own sentinels because their messages name the endpoint they know.
var errStreamSilent = errors.New("stream went silent")

// isSilentStream reports whether an error is one of the idle bounds rather than
// a transport failure. A silent stream is retryable (the endpoint may answer a
// second attempt) and is classified as a timeout in a failover record.
func isSilentStream(err error) bool {
	return errors.Is(err, errOpenAISilent) ||
		errors.Is(err, errOllamaSilent) ||
		errors.Is(err, errStreamSilent)
}

// isProtocolError reports whether an error came from reading the answer rather
// than from the transport: a body we could not parse, or an answer that carried
// no choices.
func isProtocolError(err error) bool {
	msg := err.Error()
	for _, marker := range []string{
		"parse response", "no choices returned", "ollama decode",
		"anthropic decode", "gemini decode", "bedrock decode",
		"no candidates returned", "no content blocks returned", "bedrock config", "bedrock stream",
		"marshal", "cannot unmarshal", "unexpected end of JSON",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// sleepCtx waits for the backoff, or returns early when the caller cancels.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// deliveryTracker wraps a run's callbacks and remembers whether any delta has
// already reached the caller.
//
// It exists for one decision: a streaming attempt that has delivered text may not
// be replayed. The caller has drawn it, so a second attempt would draw it twice —
// a duplicated answer is worse than the failure, which at least says what
// happened. An attempt that delivered nothing is indistinguishable from one that
// never ran, and may be repeated.
type deliveryTracker struct {
	inner     *types.StreamCallbacks
	delivered bool
	cb        *types.StreamCallbacks
}

// trackedCallbacks wraps inner. A nil inner stays nil: streaming is requested by
// the presence of callbacks, and a batch call is safe to repeat whole.
func trackedCallbacks(inner *types.StreamCallbacks) *deliveryTracker {
	t := &deliveryTracker{inner: inner}
	if inner == nil {
		return t
	}
	wrapped := *inner
	wrapped.OnReasoningDelta = func(text string) {
		t.delivered = true
		if inner.OnReasoningDelta != nil {
			inner.OnReasoningDelta(text)
		}
	}
	wrapped.OnTextDelta = func(text string) {
		t.delivered = true
		if inner.OnTextDelta != nil {
			inner.OnTextDelta(text)
		}
	}
	t.cb = &wrapped
	return t
}

// deliveredSomething reports whether the caller has already been handed output
// from the attempt that just failed.
func (t *deliveryTracker) deliveredSomething() bool { return t != nil && t.delivered }

// callbacks returns the wrapped set, or nil for a batch request.
func (t *deliveryTracker) callbacks() *types.StreamCallbacks {
	if t == nil {
		return nil
	}
	return t.cb
}

// notifyRetry tells the caller a new attempt is about to be made, so a backoff is
// not mistaken for a hang. The attempt cap travels with the event: a caller
// renders "2/3" without having to know the policy, which stays the provider's.
func notifyRetry(cb *types.StreamCallbacks, next, maxAttempts int, err error) {
	if cb != nil && cb.OnRetry != nil {
		cb.OnRetry(next, maxAttempts, err)
	}
}

// shouldRetry is the whole decision in one place: the error has to be one that a
// second attempt could fix, the caller's context has to still be alive, the
// attempt budget has to have room, and nothing may have been delivered yet.
func shouldRetry(ctx context.Context, err error, delivered bool, attempt int) bool {
	if delivered || attempt >= retryMaxAttempts {
		return false
	}
	return retryableError(ctx, err)
}

// attemptsError names how many attempts were made, so a failure that took three
// tries does not read like one that took one.
func attemptsError(attempts int, err error) error {
	if attempts <= 1 {
		return err
	}
	return fmt.Errorf("after %d attempts: %w", attempts, err)
}
