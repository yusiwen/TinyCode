package netsafe

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestIsBlockedIPTable(t *testing.T) {
	blocked := []string{
		"0.0.0.0",          // unspecified IPv4
		"0.1.2.3",          // 0.0.0.0/8 "this network"
		"::",               // unspecified IPv6
		"100.64.0.1",       // CGNAT start
		"100.100.100.200",  // CGNAT / Alibaba metadata
		"100.127.255.255",  // CGNAT end
		"127.0.0.1",        // loopback
		"127.255.255.255",  // loopback range end
		"10.0.0.1",         // RFC1918
		"172.16.0.1",       // RFC1918 start
		"172.31.255.255",   // RFC1918 end
		"192.168.1.1",      // RFC1918
		"169.254.169.254",  // link-local / cloud metadata
		"169.254.0.1",      // link-local
		"224.0.0.1",        // multicast
		"239.255.255.255",  // multicast range end
		"::1",              // IPv6 loopback
		"fe80::1",          // IPv6 link-local
		"fc00::1",          // IPv6 unique-local
		"fdff::1",          // IPv6 unique-local
		"ff02::1",          // IPv6 link-local multicast
		"::ffff:127.0.0.1", // IPv4-mapped loopback
		"::ffff:10.0.0.1",  // IPv4-mapped private
		"::127.0.0.1",      // IPv4-compatible loopback (not caught by To4)
		"::10.0.0.1",       // IPv4-compatible private
		"64:ff9b::7f00:1",  // NAT64-embedded loopback
		"64:ff9b::a00:1",   // NAT64-embedded private
		"192.0.0.1",        // 192.0.0.0/24 IETF protocol assignments
		"240.0.0.1",        // 240.0.0.0/4 reserved
		"255.255.255.255",  // broadcast
	}
	for _, c := range blocked {
		ip := net.ParseIP(c)
		if ip == nil {
			t.Fatalf("invalid test IP %q", c)
		}
		if !IsBlockedIP(ip) {
			t.Errorf("expected %s to be treated as non-public", c)
		}
	}

	if !IsBlockedIP(nil) {
		t.Error("expected a nil IP to be blocked")
	}

	public := []string{
		"1.1.1.1",
		"8.8.8.8",
		"93.184.216.34",
		"100.63.255.255", // just below CGNAT
		"100.128.0.1",    // just above CGNAT
		"172.15.255.255", // just below RFC1918
		"172.32.0.1",     // just above RFC1918
		"198.18.0.1",     // benchmarking range, deliberately allowed
		"198.51.100.1",   // TEST-NET-2
		"203.0.113.1",    // TEST-NET-3
		"2001:4860:4860::8888",
		"2606:2800:220:1:248:1893:25c8:1946",
	}
	for _, c := range public {
		ip := net.ParseIP(c)
		if ip == nil {
			t.Fatalf("invalid test IP %q", c)
		}
		if IsBlockedIP(ip) {
			t.Errorf("expected %s to be treated as public", c)
		}
	}
}

func TestCheckURLRejectsMetadataAndNonPublic(t *testing.T) {
	// Everything here is either a metadata host name from the block list or an
	// IP literal, so no DNS lookup (and no network) is needed.
	cases := []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://169.254.170.2/v2/credentials",
		"http://100.100.100.200/latest/meta-data/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://metadata.goog/",
		"http://METADATA.GOOG/", // block list is case-insensitive
		"http://127.0.0.1:8545/",
		"http://[::1]/",
		"http://0.0.0.0/",
		"http://[::]/",
		"http://10.0.0.1/admin",
		"http://192.168.1.1/admin",
		"http://172.16.0.1/",
		"http://100.64.0.1/",
	}
	for _, c := range cases {
		if err := CheckURL(c); err == nil {
			t.Errorf("CheckURL(%q) = nil, want an error", c)
		}
	}
}

func TestCheckURLRejectsNonHTTPAndMalformedURLs(t *testing.T) {
	cases := []string{
		"file:///etc/passwd",
		"ftp://example.com/",
		"data:text/plain,hi",
		"ws://example.com/socket",
		"//example.com/x", // no scheme
		"",                // empty
		"no-colon",        // no scheme
		"http://[::1",     // malformed URL (unclosed bracket)
		"http://",         // missing host
		"http:///path",    // missing host
	}
	for _, c := range cases {
		if err := CheckURL(c); err == nil {
			t.Errorf("CheckURL(%q) = nil, want an error", c)
		}
	}
}

func TestCheckURLAllowsPublicIPLiterals(t *testing.T) {
	cases := []string{
		"http://1.1.1.1/",
		"https://8.8.8.8/page",
		"http://93.184.216.34:8080/x?q=1",
		"https://[2606:2800:220:1:248:1893:25c8:1946]/",
	}
	for _, c := range cases {
		if err := CheckURL(c); err != nil {
			t.Errorf("CheckURL(%q) = %v, want nil", c, err)
		}
	}
}

// TestCheckURLBoundedContext proves the initial check cannot hang on a slow or
// hostile resolver: CheckURL always builds a bounded context, so a lookup that
// never answers still returns promptly with an error.
func TestCheckURLBoundedContext(t *testing.T) {
	previousTimeout := initialCheckTimeout
	previousLookup := lookupIPAddr
	initialCheckTimeout = 100 * time.Millisecond
	lookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() {
		initialCheckTimeout = previousTimeout
		lookupIPAddr = previousLookup
	})

	start := time.Now()
	err := CheckURL("http://stall.example/")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error when the resolver never answers")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("CheckURL did not return promptly, took %v", elapsed)
	}
}

func TestIsLoopbackHost(t *testing.T) {
	for _, host := range []string{"localhost", "LOCALHOST", "127.0.0.1", "127.1.2.3", "::1"} {
		if !IsLoopbackHost(host) {
			t.Errorf("IsLoopbackHost(%q) = false, want true", host)
		}
	}
	for _, host := range []string{"", "example.com", "10.0.0.1", "169.254.169.254", "0.0.0.0", "::"} {
		if IsLoopbackHost(host) {
			t.Errorf("IsLoopbackHost(%q) = true, want false", host)
		}
	}
}

// roundTripFunc adapts a function to http.RoundTripper so redirect handling can
// be tested without any network access.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestNewClientRejectsRedirectToMetadata(t *testing.T) {
	client := NewClient(5*time.Second, true)
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "origin.example" {
			return redirectResponse(req, "http://169.254.169.254/latest/meta-data/"), nil
		}
		return okResponse(req), nil
	})

	if _, err := client.Get("http://origin.example/start"); err == nil {
		t.Fatal("expected a redirect to the cloud metadata endpoint to be rejected")
	} else if !strings.Contains(err.Error(), "redirect blocked") {
		t.Fatalf("expected a redirect-blocked error, got: %v", err)
	}
}

func TestNewClientFollowsPublicRedirect(t *testing.T) {
	client := NewClient(5*time.Second, true)
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "origin.example" {
			return redirectResponse(req, "http://1.1.1.1/final"), nil
		}
		return okResponse(req), nil
	})

	resp, err := client.Get("http://origin.example/start")
	if err != nil {
		t.Fatalf("expected a redirect to a public IP to be followed, got: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 after redirect, got %d", resp.StatusCode)
	}
}

func TestNewClientCapsRedirectHops(t *testing.T) {
	client := NewClient(5*time.Second, true)
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return redirectResponse(req, "http://1.1.1.1/next"), nil
	})

	if _, err := client.Get("http://origin.example/start"); err == nil {
		t.Fatal("expected a redirect loop to be capped")
	} else if !strings.Contains(err.Error(), "stopped after") {
		t.Fatalf("expected a hop-cap error, got: %v", err)
	}
}

func TestNewClientPinnedDialerBlocksNonPublic(t *testing.T) {
	client := NewClient(2*time.Second, true)

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected the hardened client to use *http.Transport")
	}
	if transport.Proxy != nil {
		t.Error("expected the hardened transport to disable proxies")
	}

	// Validation happens before any socket is opened, so these never touch the
	// network.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, addr := range []string{
		"169.254.169.254:80",
		"0.0.0.0:80",
		"[::]:80",
		"100.64.0.1:80",
		"127.0.0.1:80",
		"10.0.0.1:80",
	} {
		if _, err := transport.DialContext(ctx, "tcp", addr); err == nil {
			t.Errorf("expected dialing %s to be blocked", addr)
		}
	}
}

func TestAllowLoopbackScopedToLoopbackAddresses(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addr := listener.Addr().String()

	allowed := NewClient(2*time.Second, true, AllowLoopback()).Transport.(*http.Transport)
	conn, err := allowed.DialContext(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("AllowLoopback must reach a loopback listener, got: %v", err)
	}
	conn.Close()

	// Loopback is still blocked for every other target, even with the option.
	if _, err := allowed.DialContext(ctx, "tcp", "169.254.169.254:80"); err == nil {
		t.Fatal("AllowLoopback must not unblock non-loopback targets")
	} else if !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("expected an SSRF error, got: %v", err)
	}

	strict := NewClient(2*time.Second, true).Transport.(*http.Transport)
	if _, err := strict.DialContext(ctx, "tcp", addr); err == nil {
		t.Fatal("expected loopback to be blocked without AllowLoopback")
	} else if !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("expected an SSRF error, got: %v", err)
	}
}

func TestNewClientEnforceFalseSkipsPolicy(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client := NewClient(2*time.Second, false)
	transport := client.Transport.(*http.Transport)
	conn, err := transport.DialContext(ctx, "tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("enforce=false must dial loopback, got: %v", err)
	}
	conn.Close()
}

func redirectResponse(req *http.Request, location string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusFound,
		Header:     http.Header{"Location": []string{location}},
		Body:       io.NopCloser(strings.NewReader("")),
		Request:    req,
	}
}

func okResponse(req *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("ok")),
		Request:    req,
	}
}
