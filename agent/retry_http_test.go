package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// countingServer answers with the statuses in order, repeating the last one once
// the list runs out, and counts the requests it received. The count is the
// comparison every test here uses: *how many times* the provider asked, never how
// long it waited.
func countingServer(t *testing.T, statuses []int, bodies []string) (*httptest.Server, *int32, *int32) {
	t.Helper()
	var requests int32
	var retryAfters int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&requests, 1)) - 1
		if n >= len(statuses) {
			n = len(statuses) - 1
		}
		if n == 0 && len(statuses) > 0 && statuses[0] == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "1")
			atomic.AddInt32(&retryAfters, 1)
		}
		w.WriteHeader(statuses[n])
		body := ""
		if n < len(bodies) {
			body = bodies[n]
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &requests, &retryAfters
}

// TestRetriesARateLimitThenSucceeds also pins the Retry-After floor: the endpoint
// asked for one second, and that is at least how long the pause must be.
func TestRetriesARateLimitThenSucceeds(t *testing.T) {
	fastRetries(t)
	// Callbacks are present, so this is a streaming request: the successful
	// attempt has to answer in SSE, or the parser finds no "data: " line at all.
	const okStream = "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n"
	srv, requests, _ := countingServer(t,
		[]int{http.StatusTooManyRequests, http.StatusOK},
		[]string{"slow down", okStream})

	var retries []int
	start := time.Now()
	resp, err := NewOpenAIProvider("k", srv.URL, "m").Chat(context.Background(), types.ChatRequest{
		StreamCallbacks: &types.StreamCallbacks{
			OnRetry: func(attempt, maxAttempts int, err error) { retries = append(retries, attempt) },
		},
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := atomic.LoadInt32(requests); got != 2 {
		t.Errorf("requests = %d, want 2: one rate limit, one success", got)
	}
	if resp.Content != "hello" {
		t.Errorf("content = %q, want the second attempt's answer", resp.Content)
	}
	if len(retries) != 1 || retries[0] != 2 {
		t.Errorf("OnRetry = %v, want one notice for attempt 2", retries)
	}
	if elapsed < time.Second {
		t.Errorf("elapsed = %v, want the 1s Retry-After to be honoured", elapsed)
	}
}

// TestRetriesUntilTheAttemptBudgetIsSpent: a server that never recovers must be
// asked exactly retryMaxAttempts times, and the error must say how many tries it
// took.
func TestRetriesUntilTheAttemptBudgetIsSpent(t *testing.T) {
	fastRetries(t)
	srv, requests, _ := countingServer(t,
		[]int{http.StatusServiceUnavailable},
		[]string{"try later"})

	_, err := NewOpenAIProvider("k", srv.URL, "m").Chat(context.Background(), types.ChatRequest{})
	if err == nil {
		t.Fatal("a server that never recovers must fail the call")
	}
	if got := atomic.LoadInt32(requests); got != int32(retryMaxAttempts) {
		t.Errorf("requests = %d, want exactly retryMaxAttempts = %d", got, retryMaxAttempts)
	}
	if !strings.Contains(err.Error(), "after 3 attempts") || !strings.Contains(err.Error(), "503") {
		t.Errorf("error = %v, want it to name the attempt count and the status", err)
	}
}

// TestDoesNotRetryANonRetryableStatus: a rejected key or a malformed request is
// the same answer however many times it is sent.
func TestDoesNotRetryANonRetryableStatus(t *testing.T) {
	fastRetries(t)
	srv, requests, _ := countingServer(t,
		[]int{http.StatusBadRequest},
		[]string{"bad model"})

	_, err := NewOpenAIProvider("k", srv.URL, "m").Chat(context.Background(), types.ChatRequest{})
	if err == nil {
		t.Fatal("a 400 must fail the call")
	}
	if got := atomic.LoadInt32(requests); got != 1 {
		t.Errorf("requests = %d, want 1: a 400 will not change", got)
	}
	if !strings.Contains(err.Error(), "bad model") {
		t.Errorf("error = %v, want the endpoint's message kept", err)
	}
}

// TestDoesNotReplayAStreamThatAlreadyDelivered is the safe-replay rule at the
// wire level: the caller has drawn a delta, so a second attempt would draw it
// twice. The server hijacks the connection and cuts it after the first chunk, so
// the read fails *after* delivery.
func TestDoesNotReplayAStreamThatAlreadyDelivered(t *testing.T) {
	fastRetries(t)
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n")
		chunk := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n"
		fmt.Fprintf(buf, "%x\r\n%s\r\n", len(chunk), chunk)
		buf.Flush()
		// No terminating chunk and no [DONE]: the client sees a broken stream.
	}))
	defer srv.Close()

	var deltas []string
	_, err := NewOpenAIProvider("k", srv.URL, "m").Chat(context.Background(), types.ChatRequest{
		StreamCallbacks: &types.StreamCallbacks{
			OnTextDelta: func(s string) { deltas = append(deltas, s) },
		},
	})
	if err == nil {
		t.Fatal("a stream cut mid-answer must be reported, not returned as an answer")
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("requests = %d, want 1: replaying would draw the delivered text twice", got)
	}
	if len(deltas) != 1 || deltas[0] != "Hi" {
		t.Errorf("deltas = %v, want exactly the one the caller already saw", deltas)
	}
}

// TestRetriesAStreamThatDeliveredNothing is the other half of the rule: a stream
// that fails before any delta is indistinguishable from one that never ran, so it
// may be repeated — and the delivered text must appear exactly once.
func TestRetriesAStreamThatDeliveredNothing(t *testing.T) {
	fastRetries(t)
	const body = "data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"two\"}}]}\n\n" +
		"data: [DONE]\n"
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&requests, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "loading model")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, body)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	var deltas []string
	resp, err := NewOpenAIProvider("k", srv.URL, "m").Chat(context.Background(), types.ChatRequest{
		StreamCallbacks: &types.StreamCallbacks{
			OnTextDelta: func(s string) { deltas = append(deltas, s) },
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
	if resp.Content != "onetwo" {
		t.Errorf("content = %q, want the successful attempt's text", resp.Content)
	}
	if got := strings.Join(deltas, ""); got != "onetwo" {
		t.Errorf("deltas = %q, want each token exactly once: a retry must not duplicate", got)
	}
}

// TestCancelledRunMakesNoFurtherRequest: Ctrl+C during a backoff has to stop the
// sequence, not merely be noticed after the next attempt.
func TestCancelledRunMakesNoFurtherRequest(t *testing.T) {
	prevBase := retryBaseDelay
	retryBaseDelay = 500 * time.Millisecond
	t.Cleanup(func() { retryBaseDelay = prevBase })

	firstRequest := make(chan struct{})
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&requests, 1) == 1 {
			close(firstRequest)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "busy")
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A cancelled run must not announce a retry either: the guard is in the
	// classification, and this is its observable effect (the request count alone
	// would be satisfied by the backoff's own cancellation).
	retries := 0
	errCh := make(chan error, 1)
	go func() {
		_, err := NewOpenAIProvider("k", srv.URL, "m").Chat(ctx, types.ChatRequest{
			StreamCallbacks: &types.StreamCallbacks{
				OnRetry: func(attempt, maxAttempts int, err error) { retries++ },
			},
		})
		errCh <- err
	}()

	select {
	case <-firstRequest:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never saw the first request")
	}
	cancel()

	select {
	case err := <-errCh:
		// Which error comes back is a race, and both answers are correct: a cancel
		// that lands during the backoff wraps context.Canceled, and one that lands
		// before the retry decision leaves the 503 that ended the first attempt.
		// Asserting one of them made this test fail on a loaded runner (issue
		// #193); what must hold either way is below.
		if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "503") {
			t.Errorf("error = %v, want either the caller's cancellation or the 503 that ended the first attempt", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled call did not return")
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("requests = %d, want 1: a cancelled run must not be retried", got)
	}
	if retries != 0 {
		t.Errorf("OnRetry fired %d times, want 0: a cancelled run has no next attempt to announce", retries)
	}
}

// TestOllamaRetriesATransientFailure proves the policy is shared rather than
// duplicated: the local provider gets the same treatment.
func TestOllamaRetriesATransientFailure(t *testing.T) {
	fastRetries(t)
	srv, requests, _ := countingServer(t,
		[]int{http.StatusServiceUnavailable, http.StatusOK},
		[]string{"loading model", `{"message":{"role":"assistant","content":"local answer"}}` + "\n" + `{"done":true}` + "\n"})

	var retries int
	resp, err := NewOllamaProvider(srv.URL, "m").Chat(context.Background(), types.ChatRequest{
		StreamCallbacks: &types.StreamCallbacks{
			OnRetry: func(attempt, maxAttempts int, err error) { retries++ },
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got := atomic.LoadInt32(requests); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
	if resp.Content != "local answer" {
		t.Errorf("content = %q, want the second attempt's answer", resp.Content)
	}
	if retries != 1 {
		t.Errorf("OnRetry fired %d times, want 1", retries)
	}
}

// TestOllamaDoesNotReplayADeliveredStream is the safe-replay rule on the local
// provider's stream path.
func TestOllamaDoesNotReplayADeliveredStream(t *testing.T) {
	fastRetries(t)
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/x-ndjson\r\nTransfer-Encoding: chunked\r\n\r\n")
		chunk := `{"message":{"role":"assistant","content":"half"}}` + "\n"
		fmt.Fprintf(buf, "%x\r\n%s\r\n", len(chunk), chunk)
		buf.Flush()
	}))
	defer srv.Close()

	var deltas []string
	_, err := NewOllamaProvider(srv.URL, "m").Chat(context.Background(), types.ChatRequest{
		StreamCallbacks: &types.StreamCallbacks{
			OnTextDelta: func(s string) { deltas = append(deltas, s) },
		},
	})
	if err == nil {
		t.Fatal("a stream cut mid-answer must be reported")
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
	if strings.Join(deltas, "") != "half" {
		t.Errorf("deltas = %v, want exactly what the caller already saw", deltas)
	}
}
