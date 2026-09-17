package browserproxy

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// proxyClient returns a client whose requests go through p. follow mirrors a
// browser following a redirect.
func proxyClient(t *testing.T, p *Proxy, follow bool) *http.Client {
	t.Helper()
	u, err := url.Parse(p.URL())
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	c := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(u)},
		Timeout:   5 * time.Second,
	}
	if !follow {
		c.CheckRedirect = func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}
	return c
}

// rawProxyRequest writes one request line to the proxy and returns the status
// line. It reaches paths a Go client cannot produce, such as a relative URI.
func rawProxyRequest(t *testing.T, proxyURL, requestLine string) string {
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

	if _, err := fmt.Fprintf(conn, "%s\r\nHost: target.example\r\n\r\n", requestLine); err != nil {
		t.Fatalf("write request: %v", err)
	}
	status, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	return strings.TrimSpace(status)
}

// TestForwardRelaysRequestAndResponse covers the plain HTTP path: the request
// reaches the upstream with its path intact and the response comes back with the
// end-to-end headers kept and the hop-by-hop ones stripped.
func TestForwardRelaysRequestAndResponse(t *testing.T) {
	var gotPath, gotVia, gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotVia, gotQuery = r.URL.Path, r.Header.Get("Via"), r.URL.RawQuery
		w.Header().Set("X-Upstream", "yes")
		w.Header().Set("Keep-Alive", "timeout=5")
		w.Header().Set("Proxy-Authenticate", "Basic realm=x")
		io.WriteString(w, "hello from upstream")
	}))
	defer upstream.Close()

	p, err := Start(false) // policy off: the upstream is loopback
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer p.Close()

	resp, err := proxyClient(t, p, false).Get(upstream.URL + "/some/path?q=1")
	if err != nil {
		t.Fatalf("get through proxy: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if resp.StatusCode != http.StatusOK || string(body) != "hello from upstream" {
		t.Fatalf("response = %d %q", resp.StatusCode, body)
	}
	if gotPath != "/some/path" || gotQuery != "q=1" {
		t.Errorf("upstream saw %q?%s", gotPath, gotQuery)
	}
	if gotVia == "" {
		t.Error("the proxy did not mark the forwarded request with Via")
	}
	if resp.Header.Get("X-Upstream") != "yes" {
		t.Error("an end-to-end header was dropped")
	}
	if resp.Header.Get("Keep-Alive") != "" || resp.Header.Get("Proxy-Authenticate") != "" {
		t.Errorf("hop-by-hop headers were relayed: %v", resp.Header)
	}
}

// TestForwardHandsRedirectsBack verifies the proxy does not follow a redirect
// itself: the browser has to come back through the proxy for the next hop, which
// is what makes that hop subject to the policy too.
func TestForwardHandsRedirectsBack(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "final")
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/next", http.StatusFound)
	}))
	defer redirect.Close()

	p, err := Start(false)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer p.Close()

	resp, err := proxyClient(t, p, false).Get(redirect.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want the redirect handed back", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != target.URL+"/next" {
		t.Fatalf("Location = %q", loc)
	}

	// Following it through the proxy reaches the next hop.
	resp2, err := proxyClient(t, p, true).Get(redirect.URL)
	if err != nil {
		t.Fatalf("followed get: %v", err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusOK || string(body) != "final" {
		t.Fatalf("followed response = %d %q", resp2.StatusCode, body)
	}
}

// TestBlockedTargetsAreRefused covers the policy: with enforcement on, no
// private, loopback, link-local or IPv6 loopback target is dialed.
func TestBlockedTargetsAreRefused(t *testing.T) {
	p, err := Start(true)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer p.Close()

	client := proxyClient(t, p, false)
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:1/",
		"http://10.0.0.1/",
		"http://192.168.0.1/",
		"http://[::1]:1/",
	} {
		resp, err := client.Get(target)
		if err != nil {
			t.Fatalf("GET %s: %v", target, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403", target, resp.StatusCode)
		}
	}
}

// TestRequestShapeErrors covers the two malformed request forms.
func TestRequestShapeErrors(t *testing.T) {
	p, err := Start(false)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer p.Close()

	if got := rawProxyRequest(t, p.URL(), "GET /relative HTTP/1.1"); !strings.Contains(got, "400") {
		t.Errorf("origin-form request = %q, want 400", got)
	}
	if got := rawProxyRequest(t, p.URL(), "GET ftp://example.com/file HTTP/1.1"); !strings.Contains(got, "400") {
		t.Errorf("non-http scheme = %q, want 400", got)
	}
}

// TestTunnelRefusesBlockedTarget covers the CONNECT path, which is how every
// https request arrives.
func TestTunnelRefusesBlockedTarget(t *testing.T) {
	p, err := Start(true)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer p.Close()

	for _, authority := range []string{"169.254.169.254:443", "127.0.0.1:443", "10.0.0.1:443", "[::1]:443"} {
		if got := rawProxyRequest(t, p.URL(), "CONNECT "+authority+" HTTP/1.1"); !strings.Contains(got, "403") {
			t.Errorf("CONNECT %s = %q, want 403", authority, got)
		}
	}
}

// TestTunnelReportsUpstreamFailure checks that an unreachable target is a bad
// gateway rather than a policy refusal.
func TestTunnelReportsUpstreamFailure(t *testing.T) {
	p, err := Start(false)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer p.Close()

	if got := rawProxyRequest(t, p.URL(), "CONNECT 127.0.0.1:1 HTTP/1.1"); !strings.Contains(got, "502") {
		t.Errorf("CONNECT to a closed port = %q, want 502", got)
	}
}

// TestTunnelRelaysBytes checks that an allowed CONNECT target is tunnelled in
// both directions, standing in for the TLS handshake the browser performs.
func TestTunnelRelaysBytes(t *testing.T) {
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo: %v", err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	p, err := Start(false) // the policy itself is covered above
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	defer p.Close()

	u, err := url.Parse(p.URL())
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}
	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo.Addr().String(), echo.Addr().String())
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read CONNECT status: %v", err)
	}
	if !strings.Contains(status, "200") {
		t.Fatalf("CONNECT = %q, want 200", status)
	}
	// Consume the blank line that ends the CONNECT response headers.
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read CONNECT headers: %v", err)
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write through tunnel: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo = %q, want ping", buf)
	}
}

// TestLifecycle covers the URL shape and the idempotent, effective Close.
func TestLifecycle(t *testing.T) {
	p, err := Start(false)
	if err != nil {
		t.Fatalf("start proxy: %v", err)
	}
	if !strings.HasPrefix(p.URL(), "http://127.0.0.1:") {
		t.Errorf("URL = %q, want a loopback address", p.URL())
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	u, _ := url.Parse(p.URL())
	if conn, err := net.DialTimeout("tcp", u.Host, 500*time.Millisecond); err == nil {
		conn.Close()
		t.Error("the proxy still accepts connections after Close")
	}
}
