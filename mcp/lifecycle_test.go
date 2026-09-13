package mcp

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

// TestCloseFailsPendingWaitersAndIsIdempotent ensures Close releases a blocked
// caller with a clear error, invokes the registered kill function exactly once,
// and tolerates repeated calls.
func TestCloseFailsPendingWaitersAndIsIdempotent(t *testing.T) {
	clientStdinR, clientStdinW := io.Pipe()
	serverStdoutR, serverStdoutW := io.Pipe()
	defer closePipes(clientStdinR, clientStdinW, serverStdoutR, serverStdoutW)

	client := NewClient(clientStdinW, serverStdoutR, nil)

	var kills atomic.Int32
	client.SetKillFunc(func() { kills.Add(1) })

	// Consume the request so we know it reached the peer (and is registered)
	// before Close runs.
	written := make(chan struct{})
	go func() {
		fs := newFrameStream(clientStdinR)
		if _, err := fs.read(); err != nil {
			return
		}
		close(written)
	}()

	sendErr := make(chan error, 1)
	go func() {
		_, err := client.send(context.Background(), "initialize", nil)
		sendErr <- err
	}()

	select {
	case <-written:
	case <-time.After(5 * time.Second):
		t.Fatal("request was never written")
	}

	if err := client.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := kills.Load(); got != 1 {
		t.Fatalf("expected the kill func to run exactly once, ran %d times", got)
	}

	select {
	case err := <-sendErr:
		if err == nil {
			t.Fatal("expected the pending waiter to fail after Close")
		}
		if !errors.Is(err, errClientClosed) {
			t.Fatalf("expected a closed-client error, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not release the pending waiter")
	}

	// A call after Close must fail immediately instead of hanging.
	if _, err := client.send(context.Background(), "tools/list", nil); !errors.Is(err, errClientClosed) {
		t.Fatalf("expected errClientClosed after Close, got: %v", err)
	}
}

// TestReaderFailureFailsWaiters ensures a transport that dies mid-request
// releases the waiter with the underlying read error rather than leaving it
// blocked forever.
func TestReaderFailureFailsWaiters(t *testing.T) {
	clientStdinR, clientStdinW := io.Pipe()
	serverStdoutR, serverStdoutW := io.Pipe()
	defer closePipes(clientStdinR, clientStdinW, serverStdoutR, serverStdoutW)

	client := NewClient(clientStdinW, serverStdoutR, nil)

	written := make(chan struct{})
	go func() {
		fs := newFrameStream(clientStdinR)
		if _, err := fs.read(); err != nil {
			return
		}
		close(written)
		// Close the server side of stdout without answering: the reader hits
		// EOF and must mark the client closed.
		_ = serverStdoutW.Close()
	}()

	errCh := make(chan error, 1)
	go func() {
		_, err := client.send(context.Background(), "initialize", nil)
		errCh <- err
	}()

	select {
	case <-written:
	case <-time.After(5 * time.Second):
		t.Fatal("request was never written")
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected the waiter to fail when the transport died")
		}
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected the read error to be reported, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a dead transport did not release the waiter")
	}

	if _, err := client.send(context.Background(), "tools/list", nil); err == nil {
		t.Fatal("expected later sends to fail on a closed client")
	}
}
