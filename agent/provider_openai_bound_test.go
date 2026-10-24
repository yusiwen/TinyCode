package agent

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// TestOpenAIRequestErrorClassifiesCallerCancellation pins the helper directly.
// It has to be pinned here because the subtest above runs on an HTTP/1.1
// connection, whose read error happens to wrap context.Canceled already — so
// removing the helper's own classification does not change that subtest's
// outcome. An HTTP/2 stream close (which is what HTTPS endpoints such as the
// default DeepSeek one produce) does not wrap it, and there the classification
// has to be ours: a TUI run reads "Interrupted" out of it.
func TestOpenAIRequestErrorClassifiesCallerCancellation(t *testing.T) {
	transportErr := errors.New("http2: stream closed")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	got := contextRequestError(cancelled, transportErr)
	if !errors.Is(got, context.Canceled) {
		t.Errorf("on a cancelled context, error = %v, want errors.Is(err, context.Canceled)", got)
	}
	if !errors.Is(got, transportErr) {
		t.Errorf("error = %v, want the transport error kept as well", got)
	}

	// A bound, by contrast, replaces the transport error: it is the fact the
	// reader needs, and it is what the error message has to name.
	silent, silence := context.WithCancelCause(context.Background())
	silence(errOpenAISilent)
	if got := contextRequestError(silent, transportErr); !errors.Is(got, errOpenAISilent) {
		t.Errorf("on a bound cancellation, error = %v, want the bound", got)
	}
}

// TestOpenAIStreamIdleBound covers issue #176 in the three directions its
// acceptance names: silence after finish_reason fails the call instead of
// stalling on the whole-request bound, a live stream outlives the idle bound,
// and the caller's own cancellation still classifies as one.
//
// The first case is the regression: reading past finish_reason is what makes the
// usage chunk reachable (issue #174), and without a bound of its own that read
// waits for openAIRequestTimeout — two minutes — while the run looks like it is
// still streaming an answer that is already complete.
func TestOpenAIStreamIdleBound(t *testing.T) {
	// The idle bound is retryable (issue #192), so shrink the schedule: this test
	// is about when one attempt gives up, not about how many are made.
	fastRetries(t)
	previous := openAIIdleTimeout
	openAIIdleTimeout = 400 * time.Millisecond
	t.Cleanup(func() { openAIIdleTimeout = previous })

	t.Run("silence after finish_reason fails the call", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			f := w.(http.Flusher)
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			f.Flush()
			<-release // complete answer, no usage chunk, no [DONE], no close
		}))
		t.Cleanup(func() { close(release); srv.Close() })

		start := time.Now()
		resp, err := NewOpenAIProvider("k", srv.URL, "m").Chat(context.Background(), types.ChatRequest{
			StreamCallbacks: &types.StreamCallbacks{},
		})
		elapsed := time.Since(start)

		if err == nil {
			t.Fatalf("a stream that went quiet after finish_reason returned success with %q", resp.Content)
		}
		if resp != nil {
			t.Errorf("resp = %+v, want nil: a cut stream must not be handed back as an answer", resp)
		}
		if !strings.Contains(err.Error(), "went silent") {
			t.Errorf("error = %v, want it to name the idle bound", err)
		}
		if !strings.Contains(err.Error(), openAIIdleTimeout.String()) {
			t.Errorf("error = %v, want it to name the bound %s", err, openAIIdleTimeout)
		}
		if elapsed > 5*time.Second {
			t.Errorf("the idle bound took %s to fire", elapsed)
		}
	})

	t.Run("a live stream outlives the idle bound", func(t *testing.T) {
		const chunks = 8
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			f := w.(http.Flusher)
			for i := 0; i < chunks; i++ {
				io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
				f.Flush()
				time.Sleep(openAIIdleTimeout / 4)
			}
			// The usage chunk still has to be reachable, and only then [DONE]:
			// the bound must not cut the read short of either.
			io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":8,\"total_tokens\":13}}\n\n")
			io.WriteString(w, "data: [DONE]\n\n")
			f.Flush()
		}))
		defer srv.Close()

		start := time.Now()
		resp, err := NewOpenAIProvider("k", srv.URL, "m").Chat(context.Background(), types.ChatRequest{
			StreamCallbacks: &types.StreamCallbacks{},
		})
		if err != nil {
			t.Fatalf("a stream producing a chunk every %s failed: %v", openAIIdleTimeout/4, err)
		}
		if want := strings.Repeat("x", chunks); resp.Content != want {
			t.Errorf("content = %q, want %q", resp.Content, want)
		}
		if resp.Usage == nil || resp.Usage.TotalTokens != 13 {
			t.Errorf("usage = %+v, want the reported total 13", resp.Usage)
		}
		// The exchange takes about two idle bounds in total: a single total
		// limit would have killed it, which is why the two bounds are separate.
		if elapsed := time.Since(start); elapsed <= openAIIdleTimeout {
			t.Errorf("the stream finished in %s, too fast to show the bound is idle-based", elapsed)
		}
	})

	t.Run("caller cancellation keeps its own error", func(t *testing.T) {
		// A large idle bound, so the cancellation below is what ends the read.
		previousIdle := openAIIdleTimeout
		openAIIdleTimeout = 30 * time.Second
		t.Cleanup(func() { openAIIdleTimeout = previousIdle })

		wrote := make(chan struct{})
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			f := w.(http.Flusher)
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"half\"}}]}\n\n")
			f.Flush()
			close(wrote)
			<-release
		}))
		t.Cleanup(func() { close(release); srv.Close() })

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		errCh := make(chan error, 1)
		go func() {
			_, err := NewOpenAIProvider("k", srv.URL, "m").Chat(ctx, types.ChatRequest{
				StreamCallbacks: &types.StreamCallbacks{},
			})
			errCh <- err
		}()

		select {
		case <-wrote:
		case <-time.After(5 * time.Second):
			t.Fatal("the test server never sent its first chunk")
		}
		cancel()

		select {
		case err := <-errCh:
			if err == nil {
				t.Fatal("a cancelled stream returned success")
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("error = %v, want errors.Is(err, context.Canceled) so a TUI run reads as interrupted", err)
			}
			if strings.Contains(err.Error(), "went silent") {
				t.Errorf("error = %v, want the caller's cancellation, not the idle bound", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the cancelled call did not return")
		}
	})
}

// TestOpenAIBatchTimesOutOnSilentEndpoint covers non-streaming request timeout on an endpoint
// that accepts connections but never responds.
func TestOpenAIBatchTimesOutOnSilentEndpoint(t *testing.T) {
	previous := openAIRequestTimeout
	openAIRequestTimeout = 150 * time.Millisecond
	t.Cleanup(func() { openAIRequestTimeout = previous })

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // accept the request, never answer it
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	start := time.Now()
	_, err := NewOpenAIProvider("test-key", srv.URL, "test-model").Chat(context.Background(), types.ChatRequest{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Chat against a silent endpoint returned success")
	}
	if elapsed > 5*time.Second {
		t.Errorf("Chat took %s to report the timeout", elapsed)
	}
	// Issue #186: the batch bound used to be the client's timer, whose message
	// named the transport rather than the bound. It is a watchdog on the request
	// context now, so the error says which bound fired and how long it was.
	if !strings.Contains(err.Error(), "no answer within") {
		t.Errorf("error = %v, want it to name the bound", err)
	}
	if !strings.Contains(err.Error(), openAIRequestTimeout.String()) {
		t.Errorf("error = %v, want it to name %s", err, openAIRequestTimeout)
	}
}

// TestOpenAILiveStreamOutlivesTheRequestBound is issue #185. A generation that is
// working fine must not be cut because it took longer than the batch bound: the
// server delivers a line every few milliseconds, so the idle bound is never close,
// and the answer has to survive on its own traffic alone.
//
// The control matters as much as the assertion: the stream is measured to outlast
// openAIRequestTimeout, or the test would pass without exercising the bug.
func TestOpenAILiveStreamOutlivesTheRequestBound(t *testing.T) {
	fastRetries(t)
	prevRequest, prevIdle := openAIRequestTimeout, openAIIdleTimeout
	openAIRequestTimeout = 100 * time.Millisecond
	openAIIdleTimeout = 10 * time.Second
	t.Cleanup(func() { openAIRequestTimeout, openAIIdleTimeout = prevRequest, prevIdle })

	const (
		chunks   = 10
		interval = 30 * time.Millisecond // 300ms of generation, 3x the request bound
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for i := 0; i < chunks; i++ {
			io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
			f.Flush()
			time.Sleep(interval)
		}
		io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":10,\"total_tokens\":14}}\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer srv.Close()

	start := time.Now()
	resp, err := NewOpenAIProvider("k", srv.URL, "m").Chat(context.Background(), types.ChatRequest{
		StreamCallbacks: &types.StreamCallbacks{},
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("a healthy stream delivering a line every %s was cut after %s by a %s request bound: %v",
			interval, elapsed, openAIRequestTimeout, err)
	}
	if want := strings.Repeat("x", chunks); resp.Content != want {
		t.Errorf("content = %q, want %q", resp.Content, want)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 14 {
		t.Errorf("usage = %+v, want the reported total 14 (the read must reach the usage chunk)", resp.Usage)
	}
	if elapsed <= openAIRequestTimeout {
		t.Errorf("the stream finished in %s, not longer than the %s bound: the test would not catch the regression",
			elapsed, openAIRequestTimeout)
	}
}

// TestOpenAIIdleBoundWinsWhenBothBoundsAreEqual is issue #186, at the shipped
// defaults' shape: the two bounds are equal (120s in production), and a line has
// already been delivered. The idle watchdog rebases on that line while a client
// timer would not, so a client timer always won the race — the idle bound's
// diagnosis was unreachable in every real deployment while its tests shrank only
// the idle side.
func TestOpenAIIdleBoundWinsWhenBothBoundsAreEqual(t *testing.T) {
	fastRetries(t)
	prevRequest, prevIdle := openAIRequestTimeout, openAIIdleTimeout
	openAIRequestTimeout = 400 * time.Millisecond
	openAIIdleTimeout = 400 * time.Millisecond // equal, as shipped
	t.Cleanup(func() { openAIRequestTimeout, openAIIdleTimeout = prevRequest, prevIdle })

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		// One delivered line, so the idle watchdog has rebased on it.
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n\n")
		f.Flush()
		<-release // then silence
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	start := time.Now()
	resp, err := NewOpenAIProvider("k", srv.URL, "m").Chat(context.Background(), types.ChatRequest{
		StreamCallbacks: &types.StreamCallbacks{},
	})

	if err == nil {
		t.Fatalf("a stream that went silent after one line returned success with %q", resp.Content)
	}
	if !strings.Contains(err.Error(), "went silent") {
		t.Errorf("error = %v, want the idle bound's message even though both bounds are %s", err, openAIIdleTimeout)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("the idle bound took %s to fire", elapsed)
	}
}
