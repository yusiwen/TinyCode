package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"testing"
)

// TestAnswersServerInitiatedRequests guards the reply path for requests the MCP
// server sends to us. Dropping them left the server waiting forever, which was
// documented as a known limit.
func TestAnswersServerInitiatedRequests(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()

	client := NewClient(clientWrite, clientRead, nil)
	client.startReader()
	defer func() {
		_ = client.Close()
		_ = serverRead.Close()
		_ = serverWrite.Close()
	}()

	replies := newFrameStream(serverRead)

	cases := []struct {
		method       string
		wantResult   string
		wantErrorCde int
	}{
		{method: "ping", wantResult: "{}"},
		{method: "roots/list", wantResult: `{"roots":[]}`},
		{method: "sampling/createMessage", wantErrorCde: -32601},
		{method: "something/unknown", wantErrorCde: -32601},
	}

	for i, tc := range cases {
		id := 100 + i
		req := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":{}}`, id, tc.method)
		if err := writeFrame(serverWrite, req); err != nil {
			t.Fatalf("%s: write request: %v", tc.method, err)
		}

		raw, err := replies.read()
		if err != nil {
			t.Fatalf("%s: read reply: %v", tc.method, err)
		}

		var resp jsonrpcMessage
		if err := json.Unmarshal([]byte(raw), &resp); err != nil {
			t.Fatalf("%s: parse reply %q: %v", tc.method, raw, err)
		}
		if resp.ID != id {
			t.Errorf("%s: reply id = %d, want %d (reply %s)", tc.method, resp.ID, id, raw)
		}
		if tc.wantErrorCde != 0 {
			if resp.Error == nil {
				t.Errorf("%s: expected an error reply, got %s", tc.method, raw)
			} else if resp.Error.Code != tc.wantErrorCde {
				t.Errorf("%s: error code = %d, want %d", tc.method, resp.Error.Code, tc.wantErrorCde)
			}
			continue
		}
		if resp.Error != nil {
			t.Errorf("%s: unexpected error reply: %s", tc.method, raw)
		}
		if string(resp.Result) != tc.wantResult {
			t.Errorf("%s: result = %s, want %s", tc.method, resp.Result, tc.wantResult)
		}
	}
}

// TestNotificationsAreNotAnswered ensures an id-less notification is still
// ignored (no reply is written for it).
func TestNotificationsAreNotAnswered(t *testing.T) {
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()

	client := NewClient(clientWrite, clientRead, nil)
	client.startReader()
	defer func() {
		_ = client.Close()
		_ = serverRead.Close()
		_ = serverWrite.Close()
	}()

	if err := writeFrame(serverWrite, `{"jsonrpc":"2.0","method":"notifications/message","params":{}}`); err != nil {
		t.Fatal(err)
	}
	// A request right after the notification must get exactly one reply, which
	// proves the notification itself produced none.
	if err := writeFrame(serverWrite, `{"jsonrpc":"2.0","id":7,"method":"ping"}`); err != nil {
		t.Fatal(err)
	}

	replies := newFrameStream(serverRead)
	raw, err := replies.read()
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	var resp jsonrpcMessage
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != 7 {
		t.Fatalf("expected the reply to the ping (id 7), got %s", raw)
	}
}
