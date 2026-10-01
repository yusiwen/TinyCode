package browserproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/internal/netsafe"
)

// dialRecorder stands in for the network on both proxy paths: it applies the real
// SSRF policy to the target, counts an attempt only when the policy *allowed* it,
// and then fails instead of opening a socket. A policy refusal is returned as the
// shared ErrBlocked so the proxy answers 403 exactly as it does in production.
type dialRecorder struct {
	allowedAttempts atomic.Int64
}

func (d *dialRecorder) allows(ctx context.Context, host string, enforce bool) error {
	// Bound the policy lookup exactly as the real proxy bounds a request: a
	// resolver that never answers must fail the fuzz case, not stall it.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := netsafe.ResolveValidatedHost(ctx, host, enforce); err != nil {
		return err
	}
	d.allowedAttempts.Add(1)
	return errors.New("fuzz: refusing to open a socket")
}

// Transport is the forwarded-request half (plain http:// targets).
func (d *dialRecorder) Transport() http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, d.allows(req.Context(), req.URL.Hostname(), true)
	})
}

// Dial is the CONNECT half (https:// targets).
func (d *dialRecorder) Dial(ctx context.Context, network, addr string, enforce bool, opts ...netsafe.Option) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	return nil, d.allows(ctx, host, enforce)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// FuzzProxyHandlesArbitraryTargets feeds request targets and CONNECT authorities
// straight into the proxy's handler — the attacker-influenced input this package
// parses — and asserts the two properties it exists for: no input panics it, and a
// target the policy refuses is never dialled.
//
// Both dial paths are replaced by dialRecorder, so the fuzzer cannot reach the
// network, and the only way the attempt counter moves is a target the *policy*
// allowed.
func FuzzProxyHandlesArbitraryTargets(f *testing.F) {
	for _, seed := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://127.0.0.1:1/",
		"http://10.0.0.1/",
		"http://192.168.0.1/",
		"http://[::1]:1/",
		"http://0.0.0.0:80/",
		"http://example.com/",
		"http://user:pass@example.com:8080/path?q=1#frag",
		"https://example.com/", // https arrives as CONNECT
		"CONNECT 169.254.169.254:443",
		"CONNECT 127.0.0.1:443",
		"CONNECT example.com:443",
		"CONNECT [::1]:443",
		"CONNECT example.com",
		"ftp://example.com/file",
		"file:///etc/passwd",
		"/relative",
		"*",
		"",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, target string) {
		rec := &dialRecorder{}
		p := &Proxy{
			enforce: true,
			client:  &http.Client{Transport: rec.Transport()},
			dial:    rec.Dial,
		}

		req := httptest.NewRequest(http.MethodGet, "http://proxy.test/", nil)
		req.RequestURI = target
		if authority, ok := strings.CutPrefix(target, "CONNECT "); ok {
			// The shape net/http gives a CONNECT: the authority is the host and
			// the URL is opaque.
			req.Method = http.MethodConnect
			req.Host = authority
			req.URL = &url.URL{Host: authority}
		} else if u, err := url.Parse(target); err == nil {
			req.URL = u
			req.Host = u.Host
		}

		w := httptest.NewRecorder()
		p.ServeHTTP(w, req)

		switch w.Code {
		case http.StatusBadRequest, http.StatusForbidden, http.StatusBadGateway:
		default:
			t.Fatalf("target %q produced status %d, want 400, 403 or 502", target, w.Code)
		}

		// The invariant: a refusal is a decision made *before* the network.
		if w.Code == http.StatusForbidden && rec.allowedAttempts.Load() != 0 {
			t.Fatalf("target %q was refused with 403 but reached the dialer %d time(s)",
				target, rec.allowedAttempts.Load())
		}
	})
}

// TestDialUpstreamFallsBackToTheSharedDialer pins the nil-dialer fallback: a Proxy
// built by hand (a test) still goes through the shared policy dialer, which
// refuses a link-local CONNECT target before any socket exists.
func TestDialUpstreamFallsBackToTheSharedDialer(t *testing.T) {
	p := &Proxy{enforce: true}
	_, err := p.dialUpstream(context.Background(), "169.254.169.254:443")
	if err == nil {
		t.Fatal("a link-local CONNECT target must be refused")
	}
	if !errors.Is(err, netsafe.ErrBlocked) {
		t.Errorf("error = %v, want the shared policy refusal", err)
	}
}
