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
	got := openAIRequestError(cancelled, transportErr)
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
	if got := openAIRequestError(silent, transportErr); !errors.Is(got, errOpenAISilent) {
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
}
