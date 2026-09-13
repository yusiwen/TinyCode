package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestConnConcurrentRequests verifies that concurrent Send calls each receive
// the response matching their own id, rather than racing on a shared id.
func TestConnConcurrentRequests(t *testing.T) {
	mock, conn := newMockLSP()
	defer mock.close()

	client := NewClient(conn)
	if err := client.Initialize("file:///test"); err != nil {
		t.Fatalf("init: %v", err)
	}

	const requests = 25
	var wg sync.WaitGroup
	errs := make(chan error, requests)

	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := conn.Send("test/echo", map[string]int{"i": i}); err != nil {
				errs <- err
			}
		}(i)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Send failed: %v", err)
	}
}

// TestConnCloseFailsPending verifies that Close releases a Send that is still
// waiting for a response instead of leaking the caller.
func TestConnCloseFailsPending(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	defer serverWrite.Close()

	conn := NewConn(clientWrite, clientRead)

	// Drain the request frame so Send reaches the waiting state.
	go io.Copy(io.Discard, serverRead)

	errCh := make(chan error, 1)
	go func() {
		_, err := conn.Send("never/responds", nil)
		errCh <- err
	}()

	// Give Send time to register and start waiting.
	time.Sleep(50 * time.Millisecond)

	if err := conn.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected Send to fail after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Send did not unblock after Close")
	}
}

// TestConnReadLoopFailureFailsFast verifies that when the stream dies the
// connection is marked closed, so later Sends fail immediately instead of
// waiting for the 30s request timeout.
func TestConnReadLoopFailureFailsFast(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()

	conn := NewConn(clientWrite, clientRead)
	conn.StartReader()
	go io.Copy(io.Discard, serverRead)

	// Kill the server end of the stream.
	serverWrite.Close()

	deadline := time.Now().Add(3 * time.Second)
	for !conn.isClosed() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !conn.isClosed() {
		t.Fatal("reader did not mark the connection closed after the stream ended")
	}

	start := time.Now()
	if _, err := conn.Send("test/after-death", nil); err == nil {
		t.Fatal("expected an error after the stream died")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("Send took %s after the stream died, want a fast failure", d)
	}
}

// readFrame reads one Content-Length framed message from r.
func readFrame(t *testing.T, r *bufio.Reader) []byte {
	t.Helper()
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if name, value, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(name), "Content-Length") {
			n, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil {
				t.Fatalf("bad Content-Length %q: %v", value, err)
			}
			length = n
		}
	}
	if length < 0 {
		t.Fatal("missing Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	return body
}

// frameFor renders one Content-Length framed message.
func frameFor(body string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

// TestConnAnswersServerRequests verifies that server-initiated requests are
// answered instead of being dropped (which used to stall the language server).
func TestConnAnswersServerRequests(t *testing.T) {
	cases := []struct {
		name     string
		request  string
		wantLen  int // for workspace/configuration: number of null entries
		wantNull bool
	}{
		{
			name:    "configuration",
			request: `{"jsonrpc":"2.0","id":99,"method":"workspace/configuration","params":{"items":[{},{}]}}`,
			wantLen: 2,
		},
		{
			name:     "registerCapability",
			request:  `{"jsonrpc":"2.0","id":7,"method":"client/registerCapability","params":{}}`,
			wantNull: true,
		},
		{
			name:     "unknownMethodStillAnswered",
			request:  `{"jsonrpc":"2.0","id":8,"method":"server/somethingOdd","params":{}}`,
			wantNull: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serverRead, clientWrite := io.Pipe()
			clientRead, serverWrite := io.Pipe()

			conn := NewConn(clientWrite, clientRead)
			conn.StartReader()
			defer conn.Close()

			go func() {
				// The reply reader may already have finished; a write error is
				// not fatal here and t.Fatalf must not run off the test goroutine.
				_, _ = io.WriteString(serverWrite, frameFor(tc.request))
			}()

			reply := readFrame(t, bufio.NewReader(serverRead))
			var resp struct {
				ID     json.RawMessage `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if err := json.Unmarshal(reply, &resp); err != nil {
				t.Fatalf("parse reply %q: %v", reply, err)
			}
			if len(resp.ID) == 0 {
				t.Fatalf("reply has no id: %s", reply)
			}
			if len(resp.Error) > 0 {
				t.Errorf("unexpected error in reply: %s", reply)
			}
			if tc.wantNull && string(resp.Result) != "null" {
				t.Errorf("result = %s, want null", resp.Result)
			}
			if tc.wantLen > 0 {
				var items []any
				if err := json.Unmarshal(resp.Result, &items); err != nil {
					t.Fatalf("result is not an array: %s", resp.Result)
				}
				if len(items) != tc.wantLen {
					t.Errorf("configuration reply has %d entries, want %d", len(items), tc.wantLen)
				}
			}
		})
	}
}
