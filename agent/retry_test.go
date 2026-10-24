package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// fastRetries shrinks the retry schedule to nothing for a test that provokes a
// retry. A test that counts requests should not also pay the production backoff,
// and a test about something else (a bound, a usage record) should not acquire a
// multi-second tail now that the retry path runs underneath it.
func fastRetries(t *testing.T) {
	t.Helper()
	prevAttempts, prevBase, prevJitter := retryMaxAttempts, retryBaseDelay, retryJitter
	retryBaseDelay = time.Millisecond
	retryJitter = func() float64 { return 0 }
	t.Cleanup(func() {
		retryMaxAttempts, retryBaseDelay, retryJitter = prevAttempts, prevBase, prevJitter
	})
}

// TestBackoffDelaySchedule pins the shape an operator reasons about: growth,
// a cap, and a floor the endpoint can raise.
func TestBackoffDelaySchedule(t *testing.T) {
	prevBase, prevMax, prevJitter := retryBaseDelay, retryMaxDelay, retryJitter
	retryBaseDelay, retryMaxDelay = 100*time.Millisecond, 400*time.Millisecond
	t.Cleanup(func() { retryBaseDelay, retryMaxDelay, retryJitter = prevBase, prevMax, prevJitter })

	// Jitter at zero gives the fixed half of equal jitter: exactly half the
	// window, so the schedule is assertable line by line.
	retryJitter = func() float64 { return 0 }
	want := []time.Duration{50, 100, 200, 200, 200} // milliseconds: 100,200,400 capped
	for i, w := range want {
		if got := backoffDelay(i+1, 0); got != w*time.Millisecond {
			t.Errorf("backoffDelay(attempt %d) = %v, want %v", i+1, got, w*time.Millisecond)
		}
	}

	// Jitter at one gives the top of the window: never above it.
	retryJitter = func() float64 { return 1 }
	if got := backoffDelay(1, 0); got != 100*time.Millisecond {
		t.Errorf("with full jitter the first delay = %v, want the whole window 100ms", got)
	}
	if got := backoffDelay(4, 0); got > retryMaxDelay {
		t.Errorf("delay = %v, want it capped at %v", got, retryMaxDelay)
	}

	// An endpoint that asked for longer than the window gets it, and a request
	// for longer than the cap is capped.
	retryJitter = func() float64 { return 0 }
	if got := backoffDelay(1, 300*time.Millisecond); got != 300*time.Millisecond {
		t.Errorf("delay = %v, want the endpoint's 300ms to win over the 50ms window", got)
	}
	if got := backoffDelay(1, time.Hour); got != retryMaxDelay {
		t.Errorf("delay = %v, want an hour-long Retry-After capped at %v", got, retryMaxDelay)
	}

	// Attempt zero or negative is treated as the first attempt rather than
	// producing a negative shift.
	if got := backoffDelay(0, 0); got != 50*time.Millisecond {
		t.Errorf("backoffDelay(0) = %v, want the first attempt's delay", got)
	}
}

// TestParseRetryAfter covers the form that is honoured and the form that is
// deliberately ignored: a mis-parsed HTTP date would be an arbitrary pause.
func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		header string
		want   time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"-5", 0},
		{"2", 2 * time.Second},
		{" 7 ", 7 * time.Second},
		{"soon", 0},
		{"Wed, 21 Oct 2026 07:28:00 GMT", 0}, // HTTP-date: ignored on purpose
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.header); got != tc.want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestStatusErrorRetryable(t *testing.T) {
	retryable := []int{408, 429, 500, 502, 503, 504, 599}
	for _, status := range retryable {
		if e := (&statusError{status: status}); !e.Retryable() {
			t.Errorf("status %d: Retryable() = false, want true", status)
		}
	}
	final := []int{400, 401, 403, 404, 409, 413, 422}
	for _, status := range final {
		if e := (&statusError{status: status}); e.Retryable() {
			t.Errorf("status %d: Retryable() = true, want false: that answer will not change", status)
		}
	}
}

// TestRetryableErrorClassification is the whole decision table in one place.
func TestRetryableErrorClassification(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name string
		ctx  context.Context
		err  error
		want bool
	}{
		{"nil", context.Background(), nil, false},
		{"rate limit", context.Background(), &statusError{status: 429}, true},
		{"server error", context.Background(), &statusError{status: 503}, true},
		{"bad request", context.Background(), &statusError{status: 400}, false},
		{"transport", context.Background(), errors.New("dial tcp: connection refused"), true},
		{"idle bound", context.Background(), errOpenAISilent, true},
		{"ollama idle bound", context.Background(), errOllamaSilent, true},
		{"native idle bound", context.Background(), errStreamSilent, true},
		{"protocol", context.Background(), errors.New("parse response: unexpected end of JSON"), false},
		{"no choices", context.Background(), errors.New("no choices returned"), false},
		{"caller cancelled", cancelled, errors.New("dial tcp: connection refused"), false},
		{"context error", context.Background(), context.DeadlineExceeded, false},
		{"cancelled context error", context.Background(), context.Canceled, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := retryableError(tc.ctx, tc.err); got != tc.want {
				t.Errorf("retryableError = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestShouldRetryAlsoChecksDeliveryAndBudget: the error table alone is not the
// decision.
func TestShouldRetryAlsoChecksDeliveryAndBudget(t *testing.T) {
	transient := &statusError{status: 503}
	if !shouldRetry(context.Background(), transient, false, 1) {
		t.Error("a transient failure before anything was delivered should be retried")
	}
	if shouldRetry(context.Background(), transient, true, 1) {
		t.Error("a stream that already delivered must not be replayed")
	}
	prev := retryMaxAttempts
	retryMaxAttempts = 2
	t.Cleanup(func() { retryMaxAttempts = prev })
	if shouldRetry(context.Background(), transient, false, 2) {
		t.Error("the last attempt must not be retried")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if shouldRetry(cancelled, transient, false, 1) {
		t.Error("a cancelled run must not be retried")
	}
}

// TestDeliveryTracker pins what counts as delivered and what is forwarded
// untouched.
func TestDeliveryTracker(t *testing.T) {
	// A batch call: no callbacks, so nothing can be delivered and the provider
	// must still see nil (streaming is requested by their presence).
	if tracker := trackedCallbacks(nil); tracker.callbacks() != nil || tracker.deliveredSomething() {
		t.Error("a nil callback set must stay nil and undelivered")
	}

	var text, reasoning []string
	usageFired, costFired := 0, 0
	inner := &types.StreamCallbacks{
		OnTextDelta:      func(s string) { text = append(text, s) },
		OnReasoningDelta: func(s string) { reasoning = append(reasoning, s) },
		OnUsage:          func(types.Usage) { usageFired++ },
		OnCost:           func(types.CostEvent) { costFired++ },
	}
	tracker := trackedCallbacks(inner)
	if tracker.deliveredSomething() {
		t.Fatal("nothing has been delivered before the first delta")
	}

	tracker.callbacks().OnReasoningDelta("thinking")
	if !tracker.deliveredSomething() {
		t.Error("a reasoning delta reaches the caller, so it counts as delivered")
	}
	if len(reasoning) != 1 {
		t.Errorf("reasoning deltas = %v, want the delta forwarded", reasoning)
	}

	tracker.callbacks().OnTextDelta("Hi")
	if len(text) != 1 || text[0] != "Hi" {
		t.Errorf("text deltas = %v, want the delta forwarded", text)
	}

	// The callbacks that do not reach the user are forwarded unchanged and do not
	// count as delivery: a usage record is accounting, not an answer.
	tracker.callbacks().OnUsage(types.Usage{TotalTokens: 1})
	tracker.callbacks().OnCost(types.CostEvent{Source: types.CostReported})
	if usageFired != 1 || costFired != 1 {
		t.Errorf("usage/cost forwarded %d/%d times, want 1/1", usageFired, costFired)
	}
}

// TestAttemptsErrorNamesTheCount: a failure that took three tries must not read
// like one that took a single try.
func TestAttemptsErrorNamesTheCount(t *testing.T) {
	base := errors.New("status 503: busy")
	if got := attemptsError(1, base); got != base {
		t.Errorf("one attempt should return the error unchanged, got %v", got)
	}
	got := attemptsError(3, base)
	if !strings.Contains(got.Error(), "after 3 attempts") || !errors.Is(got, base) {
		t.Errorf("error = %v, want it to name the count and wrap the cause", got)
	}
}

// TestSleepCtxReturnsEarlyOnCancel keeps a backoff from outliving its caller.
func TestSleepCtxReturnsEarlyOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := sleepCtx(ctx, time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("sleepCtx waited %v, want it to return when the caller cancels", elapsed)
	}
	if err := sleepCtx(context.Background(), 0); err != nil {
		t.Errorf("a zero delay = %v, want no wait", err)
	}
}
