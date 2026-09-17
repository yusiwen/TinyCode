package tool

import (
	"bufio"
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestBrowserProxyFlags pins the flags that route Chromium through the filtering
// proxy: the proxy address, the loopback-bypass override (Chromium skips a proxy
// for loopback by default) and QUIC off (HTTP/3 leaves over UDP).
func TestBrowserProxyFlags(t *testing.T) {
	valued, bare := browserProxyFlags("http://127.0.0.1:12345")
	if len(valued) != 2 {
		t.Fatalf("valued flags = %v, want two entries", valued)
	}
	if valued[0][0] != "proxy-server" || valued[0][1] != "http://127.0.0.1:12345" {
		t.Errorf("proxy-server flag = %v", valued[0])
	}
	if valued[1][0] != "proxy-bypass-list" || valued[1][1] != "<-loopback>" {
		t.Errorf("loopback must not bypass the proxy: %v", valued[1])
	}
	if len(bare) != 1 || bare[0] != "disable-quic" {
		t.Errorf("bare flags = %v, want disable-quic", bare)
	}

	args := appendBrowserProxyArgs([]string{"--headless"}, "http://127.0.0.1:1")
	want := []string{
		"--headless",
		"--proxy-server=http://127.0.0.1:1",
		"--proxy-bypass-list=<-loopback>",
		"--disable-quic",
	}
	if strings.Join(args, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v, want %v", args, want)
	}
}

// connectStatus asks the proxy for a CONNECT tunnel and returns its status line.
func connectStatus(t *testing.T, proxyURL, authority string) string {
	t.Helper()
	u, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", authority, authority); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	return strings.TrimSpace(line)
}

// TestStartBrowserProxyHonoursSSRFHook verifies the wiring of the package-wide
// test hook: with checks enabled a reachable loopback target is refused, and
// only the hook turns that off (the proxy is otherwise unusable by tests, which
// talk to local listeners).
func TestStartBrowserProxyHonoursSSRFHook(t *testing.T) {
	previous := skipSSRFCheck
	t.Cleanup(func() { skipSSRFCheck = previous })

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer target.Close()
	go func() {
		for {
			conn, err := target.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	skipSSRFCheck = false
	enforced := startBrowserProxy()
	if enforced == nil {
		t.Fatal("the enforced proxy did not start")
	}
	defer enforced.Close()
	if got := connectStatus(t, enforced.URL(), target.Addr().String()); !strings.Contains(got, "403") {
		t.Errorf("CONNECT to a live loopback target = %q, want 403", got)
	}

	skipSSRFCheck = true
	unenforced := startBrowserProxy()
	if unenforced == nil {
		t.Fatal("the test proxy did not start")
	}
	defer unenforced.Close()
	if got := connectStatus(t, unenforced.URL(), target.Addr().String()); !strings.Contains(got, "200") {
		t.Errorf("CONNECT with the test hook = %q, want 200", got)
	}
}
