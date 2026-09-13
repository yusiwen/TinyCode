package tool

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// withSSRFEnforced temporarily turns the package-wide skipSSRFCheck test hook
// off so the SSRF policy is actually exercised, and restores it afterwards.
func withSSRFEnforced(t *testing.T) {
	t.Helper()
	previous := skipSSRFCheck
	skipSSRFCheck = false
	t.Cleanup(func() { skipSSRFCheck = previous })
}

// roundTripFunc adapts a function to http.RoundTripper so redirect handling can
// be tested without any network access.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestIsPrivateIPRejectsNonPublicRanges(t *testing.T) {
	cases := []string{
		"0.0.0.0",          // unspecified IPv4
		"0.1.2.3",          // 0.0.0.0/8 "this network"
		"::",               // unspecified IPv6
		"100.64.0.1",       // CGNAT start
		"100.100.100.200",  // CGNAT / Alibaba metadata
		"100.127.255.255",  // CGNAT end
		"127.0.0.1",        // loopback
		"10.0.0.1",         // RFC1918
		"172.16.0.1",       // RFC1918
		"192.168.1.1",      // RFC1918
		"169.254.169.254",  // link-local / cloud metadata
		"224.0.0.1",        // multicast
		"::1",              // IPv6 loopback
		"fe80::1",          // IPv6 link-local
		"fc00::1",          // IPv6 unique-local
		"ff02::1",          // IPv6 link-local multicast
		"::ffff:127.0.0.1", // IPv4-mapped loopback
		"::127.0.0.1",      // IPv4-compatible loopback (not caught by To4)
		"::10.0.0.1",       // IPv4-compatible private
		"64:ff9b::7f00:1",  // NAT64-embedded loopback
		"192.0.0.1",        // 192.0.0.0/24 IETF protocol assignments
		"240.0.0.1",        // 240.0.0.0/4 reserved
		"255.255.255.255",  // broadcast
	}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if ip == nil {
			t.Fatalf("invalid test IP %q", c)
		}
		if !isPrivateIP(ip) {
			t.Errorf("expected %s to be treated as non-public", c)
		}
	}
}

func TestIsPrivateIPAllowsPublicRanges(t *testing.T) {
	cases := []string{
		"1.1.1.1",
		"8.8.8.8",
		"100.63.255.255", // just below CGNAT
		"100.128.0.1",    // just above CGNAT
		"2001:4860:4860::8888",
	}
	for _, c := range cases {
		ip := net.ParseIP(c)
		if ip == nil {
			t.Fatalf("invalid test IP %q", c)
		}
		if isPrivateIP(ip) {
			t.Errorf("expected %s to be treated as public", c)
		}
	}
}

func TestCheckSSRFRejectsNonPublicURLs(t *testing.T) {
	// All of these are IP literals, so no DNS lookup (and no network) is needed.
	cases := []string{
		"http://0.0.0.0/",
		"http://[::]/",
		"http://100.64.0.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://192.168.1.1/admin",
		"http://[::1]:9000/",
	}
	for _, c := range cases {
		if err := checkSSRF(c); err == nil {
			t.Errorf("expected %s to be rejected by the SSRF validator", c)
		}
	}
}

func TestCheckSSRFAllowsPublicURL(t *testing.T) {
	for _, c := range []string{"http://1.1.1.1/", "https://8.8.8.8/page"} {
		if err := checkSSRF(c); err != nil {
			t.Errorf("expected public URL %s to pass the SSRF validator, got: %v", c, err)
		}
	}
}

func TestSSRFProtectedClientRejectsRedirectToMetadata(t *testing.T) {
	withSSRFEnforced(t)

	client := newSSRFProtectedClient(5 * time.Second)
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

func TestSSRFProtectedClientFollowsPublicRedirect(t *testing.T) {
	withSSRFEnforced(t)

	client := newSSRFProtectedClient(5 * time.Second)
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

func TestSSRFProtectedClientCapsRedirectHops(t *testing.T) {
	withSSRFEnforced(t)

	client := newSSRFProtectedClient(5 * time.Second)
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return redirectResponse(req, "http://1.1.1.1/next"), nil
	})

	if _, err := client.Get("http://origin.example/start"); err == nil {
		t.Fatal("expected a redirect loop to be capped")
	} else if !strings.Contains(err.Error(), "stopped after") {
		t.Fatalf("expected a hop-cap error, got: %v", err)
	}
}

func TestSSRFDialContextBlocksNonPublicIPs(t *testing.T) {
	withSSRFEnforced(t)

	client := newSSRFProtectedClient(2 * time.Second)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected the hardened client to use *http.Transport")
	}

	// Validation happens before any socket is opened, so these never touch the
	// network.
	for _, addr := range []string{
		"169.254.169.254:80",
		"0.0.0.0:80",
		"[::]:80",
		"100.64.0.1:80",
		"127.0.0.1:80",
	} {
		if _, err := transport.DialContext(context.Background(), "tcp", addr); err == nil {
			t.Errorf("expected dialing %s to be blocked", addr)
		}
	}
}

func TestSSRFProtectedClientReachesLocalTestServer(t *testing.T) {
	// The package-wide skipSSRFCheck hook (set by web_extract_test.go) allows
	// loopback origins used by httptest; the pinned dialer must still work.
	previous := skipSSRFCheck
	skipSSRFCheck = true
	t.Cleanup(func() { skipSSRFCheck = previous })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	defer server.Close()

	client := newSSRFProtectedClient(5 * time.Second)
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("expected the hardened client to reach the test server, got: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello" {
		t.Fatalf("expected body %q, got %q", "hello", string(body))
	}
}

func TestCheckMCPURLRejectsUnspecifiedAndNonPublic(t *testing.T) {
	// IP literals only: no DNS lookup required.
	cases := []string{
		"http://0.0.0.0:9000/mcp",
		"http://[::]:9000/mcp",
		"http://100.64.0.1:9000/mcp",
		"http://169.254.169.254/mcp",
		"http://192.168.1.1:9000/mcp",
	}
	for _, c := range cases {
		if err := checkMCPURL(c); err == nil {
			t.Errorf("expected MCP URL %s to be rejected", c)
		}
	}
}

func TestCheckMCPURLAllowsLocalhost(t *testing.T) {
	if err := checkMCPURL("http://127.0.0.1:9000/mcp"); err != nil {
		t.Fatalf("expected localhost MCP URL to be allowed, got: %v", err)
	}
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

// TestCheckBrowserTargetValidatesAddr guards the browser pre-flight: a
// non-public target must be refused before Chromium is launched.
func TestCheckBrowserTargetValidatesAddr(t *testing.T) {
	saved := skipSSRFCheck
	skipSSRFCheck = false
	defer func() { skipSSRFCheck = saved }()

	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:8080/",
		"http://[::1]/",
		"file:///etc/passwd",
	} {
		if err := checkBrowserTarget(u); err == nil {
			t.Errorf("checkBrowserTarget(%q) = nil, want an error", u)
		}
	}

	// The test hook still short-circuits everything.
	skipSSRFCheck = true
	if err := checkBrowserTarget("http://127.0.0.1:8080/"); err != nil {
		t.Errorf("skipSSRFCheck=true should bypass the check, got %v", err)
	}
}

// TestBrowserRequestAllowed covers the in-browser request predicate. It uses IP
// literals only, so no DNS lookup (and no network) is required.
func TestBrowserRequestAllowed(t *testing.T) {
	previous := skipSSRFCheck
	skipSSRFCheck = false
	defer func() { skipSSRFCheck = previous }()

	public := []string{
		"http://93.184.216.34/",
		"https://93.184.216.34/page?q=1",
	}
	for _, u := range public {
		if err := browserRequestAllowed(u); err != nil {
			t.Errorf("browserRequestAllowed(%q) = %v, want nil", u, err)
		}
	}

	rejected := []string{
		"http://169.254.169.254/latest/meta-data/", // cloud metadata
		"http://127.0.0.1/",                        // loopback
		"http://[::1]/",                            // IPv6 loopback
		"http://0.0.0.0/",                          // unspecified
		"file:///etc/passwd",                       // non-http scheme
		"ftp://93.184.216.34/",                     // non-http scheme
		"http://[::1",                              // malformed URL (unclosed bracket)
		"",                                         // empty URL
	}
	for _, u := range rejected {
		if err := browserRequestAllowed(u); err == nil {
			t.Errorf("browserRequestAllowed(%q) = nil, want an error", u)
		}
	}

	// The test hook disables the predicate for every input, public or not.
	skipSSRFCheck = true
	for _, u := range append(public, rejected...) {
		if err := browserRequestAllowed(u); err != nil {
			t.Errorf("skipSSRFCheck=true: browserRequestAllowed(%q) = %v, want nil", u, err)
		}
	}
}

// TestBrowserHijackDecisionFor pins the abort/continue mapping used by the rod
// interception handler.
func TestBrowserHijackDecisionFor(t *testing.T) {
	previous := skipSSRFCheck
	skipSSRFCheck = false
	defer func() { skipSSRFCheck = previous }()

	if got := browserHijackDecisionFor("http://93.184.216.34/"); got != hijackContinue {
		t.Errorf("public URL decision = %v, want hijackContinue", got)
	}
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1/",
		"http://[::1]/",
		"file:///etc/passwd",
		"http://[::1",
	} {
		if got := browserHijackDecisionFor(u); got != hijackAbort {
			t.Errorf("browserHijackDecisionFor(%q) = %v, want hijackAbort", u, got)
		}
	}

	// The hook also bypasses the hijack decision.
	skipSSRFCheck = true
	if got := browserHijackDecisionFor("http://169.254.169.254/latest/meta-data/"); got != hijackContinue {
		t.Errorf("skipSSRFCheck=true decision = %v, want hijackContinue", got)
	}
}

// TestBrowserRequestAllowedNonNetworkSchemes covers local-only schemes: inline
// data:/blob: subresources must not be aborted (they never touch the network),
// while file: and network schemes other than http(s) must be.
func TestBrowserRequestAllowedNonNetworkSchemes(t *testing.T) {
	saved := skipSSRFCheck
	skipSSRFCheck = false
	defer func() { skipSSRFCheck = saved }()

	allowed := []string{
		"data:image/png;base64,iVBORw0KGgo=",
		"blob:https://example.com/1234",
		"about:blank",
	}
	for _, u := range allowed {
		if err := browserRequestAllowed(u); err != nil {
			t.Errorf("browserRequestAllowed(%q) = %v, want nil", u, err)
		}
	}

	blocked := []string{
		"file:///etc/passwd",
		"ftp://example.com/x",
		"ws://example.com/socket",
		"http://169.254.169.254/",
	}
	for _, u := range blocked {
		if err := browserRequestAllowed(u); err == nil {
			t.Errorf("browserRequestAllowed(%q) = nil, want an error", u)
		}
	}
}

// TestUrlScheme covers the small scheme parser used by the predicate.
func TestUrlScheme(t *testing.T) {
	cases := map[string]string{
		"HTTP://Example.com/": "http",
		"data:text/plain,hi":  "data",
		"//example.com/x":     "",
		"no-colon":            "",
		"we ird:x":            "",
		"":                    "",
	}
	for in, want := range cases {
		if got := urlScheme(in); got != want {
			t.Errorf("urlScheme(%q) = %q, want %q", in, got, want)
		}
	}
}
