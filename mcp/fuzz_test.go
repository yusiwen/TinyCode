package mcp

import (
	"bytes"
	"io"
	"testing"
)

// FuzzReadMessageBounds feeds arbitrary bytes to the Content-Length frame reader.
// A peer controls this stream, so the reader must never panic, must never return
// a body larger than maxMessageSize, and must reject anything it cannot frame.
func FuzzReadMessageBounds(f *testing.F) {
	f.Add([]byte("Content-Length: 5\r\n\r\nhello"))
	f.Add([]byte("Content-Length: 0\r\n\r\n"))
	f.Add([]byte("content-length: 3\r\n\r\nabc"))
	f.Add([]byte("Content-Length: 3\n\nabc"))
	f.Add([]byte("X-Ignored: 1\r\nContent-Length: 2\r\n\r\nhi"))
	f.Add([]byte("Content-Length: 999999999\r\n\r\n"))
	f.Add([]byte("Content-Length: -1\r\n\r\n"))
	f.Add([]byte("Content-Length: abc\r\n\r\n"))
	f.Add([]byte("\r\n\r\n"))
	f.Add([]byte("Content-Length: 10\r\n\r\nshort"))
	f.Add([]byte("Content-Length: 2\r\n\r\n\u00e9"))

	f.Fuzz(func(t *testing.T, data []byte) {
		c := NewClient(io.Discard, bytes.NewReader(data), nil)
		body, err := c.readMessage()
		if err != nil {
			return
		}
		if len(body) > maxMessageSize {
			t.Fatalf("readMessage returned %d bytes, above the %d byte limit", len(body), maxMessageSize)
		}
		// A readable frame must consume its declared body from the stream.
		rest, _ := io.ReadAll(c.stdout)
		if len(data) > 0 && len(body)+len(rest) > len(data) {
			t.Fatalf("frame read more bytes than the input holds: body=%d rest=%d input=%d", len(body), len(rest), len(data))
		}
	})
}
