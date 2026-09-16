// Package netsafe centralizes the SSRF (server-side request forgery) policy
// shared by the web tools and the MCP HTTP transport.
//
// The policy has two layers:
//
//   - CheckURL and ResolveValidatedHost validate a URL or host before it is
//     used.
//   - NewClient returns an *http.Client whose transport resolves each host
//     exactly once, validates every resolved address and then pins the
//     connection to an already-validated IP. A DNS-rebinding attacker therefore
//     cannot swap the destination between the check and the connect. Every
//     redirect target is re-validated with the same policy and the hop count is
//     capped.
package netsafe

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// maxSSRFRedirects caps how many HTTP redirect hops a hardened client follows.
const maxSSRFRedirects = 5

// initialCheckTimeout bounds the DNS resolution performed by CheckURL so a slow
// or hostile resolver cannot stall a tool call. It is a variable so tests can
// shrink it without waiting for real timeouts.
var initialCheckTimeout = 5 * time.Second

// Transport tuning constants used by NewClient.
const (
	dialTimeout           = 10 * time.Second
	dialKeepAlive         = 30 * time.Second
	maxIdleConns          = 16
	idleConnTimeout       = 30 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	expectContinueTimeout = time.Second
)

// blockedHosts lists cloud metadata endpoints that must never be fetched. The
// IP entries are redundant with the address checks below but make the intent
// explicit and survive future policy edits.
var blockedHosts = map[string]bool{
	"169.254.169.254":          true,
	"169.254.170.2":            true,
	"169.254.169.253":          true,
	"metadata.google.internal": true,
	"metadata.goog":            true,
	"100.100.100.200":          true,
}

// lookupIPAddr resolves a host to its addresses. It is a package variable so
// tests can simulate a stalled resolver without touching the real network.
var lookupIPAddr = net.DefaultResolver.LookupIPAddr

// CheckURL validates rawURL against the SSRF policy. Cloud metadata hostnames
// and any host resolving to a private, loopback, link-local, unspecified,
// CGNAT, broadcast or multicast address are rejected.
//
// CheckURL always enforces: there is no opt-out. Its DNS resolution runs under
// a bounded context so a slow or hostile resolver cannot stall the caller.
func CheckURL(rawURL string) error {
	return checkURL(rawURL, false)
}

// checkURL validates rawURL with a bounded context. allowLoopback is only set by
// callers that deliberately target a local endpoint (see AllowLoopback).
func checkURL(rawURL string, allowLoopback bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), initialCheckTimeout)
	defer cancel()
	return checkURLContext(ctx, rawURL, allowLoopback)
}

func checkURLContext(ctx context.Context, rawURL string, allowLoopback bool) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("SSRF: unsupported URL scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("SSRF: missing host in %q", rawURL)
	}
	if blockedHosts[strings.ToLower(host)] {
		return fmt.Errorf("SSRF: blocked host %q (cloud metadata)", host)
	}
	if _, err := resolveValidatedHost(ctx, host, true, allowLoopback); err != nil {
		return err
	}
	return nil
}

// ResolveValidatedHost resolves host exactly once. When enforce is true every
// resolved address must pass the SSRF policy, so a hostname that mixes public
// and private addresses is rejected instead of silently dialed.
func ResolveValidatedHost(ctx context.Context, host string, enforce bool) ([]net.IP, error) {
	return resolveValidatedHost(ctx, host, enforce, false)
}

// resolveValidatedHost is the internal form; allowLoopback additionally permits
// loopback addresses.
func resolveValidatedHost(ctx context.Context, host string, enforce, allowLoopback bool) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if enforce {
			if err := validatePublicIP(ip, host, allowLoopback); err != nil {
				return nil, err
			}
		}
		return []net.IP{ip}, nil
	}

	addrs, err := lookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("SSRF: DNS resolution failed for %q: %w", host, err)
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if enforce {
			if err := validatePublicIP(addr.IP, host, allowLoopback); err != nil {
				return nil, err
			}
		}
		ips = append(ips, addr.IP)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("SSRF: no addresses resolved for host %q", host)
	}
	return ips, nil
}

// validatePublicIP rejects any address that is not a globally routable unicast
// IP. When allowLoopback is set, loopback addresses are accepted as well.
func validatePublicIP(ip net.IP, host string, allowLoopback bool) error {
	if allowLoopback && ip.IsLoopback() {
		return nil
	}
	if IsBlockedIP(ip) {
		return fmt.Errorf("SSRF: blocked non-public IP %q for host %q", ip.String(), host)
	}
	return nil
}

// IsBlockedIP reports whether ip is outside the set of addresses the SSRF
// policy allows: nil, private, loopback, link-local, unspecified, CGNAT,
// multicast, broadcast and reserved addresses are all blocked, including the
// IPv4-mapped, IPv4-compatible and NAT64 forms that embed such an IPv4 address.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	// IPv4 and IPv4-mapped IPv6 addresses are checked explicitly so the
	// special ranges below cannot be smuggled in as ::ffff:a.b.c.d.
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 0: // 0.0.0.0/8 "this network" (includes 0.0.0.0)
			return true
		case ip4[0] == 10: // 10.0.0.0/8
			return true
		case ip4[0] == 127: // 127.0.0.0/8 loopback
			return true
		case ip4[0] == 169 && ip4[1] == 254: // 169.254.0.0/16 link-local / metadata
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31: // 172.16.0.0/12
			return true
		case ip4[0] == 192 && ip4[1] == 168: // 192.168.0.0/16
			return true
		case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127: // 100.64.0.0/10 CGNAT
			return true
		// Note: 198.18.0.0/15 is deliberately NOT blocked. Sandboxes and
		// transparent egress proxies commonly map public names into that
		// benchmarking range, so blocking it breaks legitimate fetches.
		case ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0: // 192.0.0.0/24 IETF protocol assignments
			return true
		case ip4[0] >= 240: // 240.0.0.0/4 reserved, includes 255.255.255.255
			return true
		}
	}

	// IPv4-compatible IPv6 (::a.b.c.d) is not covered by To4, and NAT64
	// (64:ff9b::/96) embeds an IPv4 address; both could smuggle a loopback or
	// private target past the checks above.
	if ip16 := ip.To16(); ip16 != nil {
		allZero := true
		for _, b := range ip16[:12] {
			if b != 0 {
				allZero = false
				break
			}
		}
		if allZero {
			return true
		}
		if ip16[0] == 0x00 && ip16[1] == 0x64 && ip16[2] == 0xff && ip16[3] == 0x9b {
			return true
		}
	}

	// Covers IPv6 loopback, link-local, unique-local, unspecified (::),
	// multicast, and link-local multicast ranges; !IsGlobalUnicast also rejects
	// the IPv4/6 broadcast and other non-unicast forms.
	return !ip.IsGlobalUnicast() ||
		ip.IsUnspecified() ||
		ip.IsLoopback() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsPrivate()
}

// IsLoopbackHost reports whether host is the name "localhost" or an IP literal
// in a loopback range. It lets a caller decide whether a deliberately local
// endpoint is being configured.
func IsLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// Option configures a client returned by NewClient.
type Option func(*clientConfig)

type clientConfig struct {
	// allowedAuthority is the single host:port whose loopback addresses may be
	// reached. Empty means loopback is blocked everywhere.
	allowedAuthority string
}

// AllowAuthority permits loopback addresses for one authority (host:port) while
// every other SSRF rule stays in force. It exists for callers whose configured
// endpoint is deliberately local, such as a localhost MCP development server.
//
// The exemption is deliberately scoped to that authority rather than the whole
// client: without it, a local endpoint could redirect the client to any other
// loopback service (a container API, an admin port, a database), which is an
// SSRF primitive we do not want to hand out.
func AllowAuthority(hostport string) Option {
	return func(c *clientConfig) { c.allowedAuthority = normalizeAuthority(hostport) }
}

// normalizeAuthority canonicalizes a host:port for comparison: IPv6 literals are
// unbracketed and re-canonicalized, host names are lower-cased and the port is
// kept verbatim.
func normalizeAuthority(authority string) string {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		host, port = authority, ""
	}
	host = strings.Trim(host, "[]")
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else {
		host = strings.ToLower(host)
	}
	return net.JoinHostPort(host, port)
}

// loopbackAllowed reports whether the loopback exemption covers host:port.
func (c *clientConfig) loopbackAllowed(host, port string) bool {
	if c.allowedAuthority == "" {
		return false
	}
	return normalizeAuthority(net.JoinHostPort(strings.Trim(host, "[]"), port)) == c.allowedAuthority
}

// NewClient returns an *http.Client hardened against SSRF.
//
// The target host is resolved exactly once per connection, every resolved IP is
// validated against the shared SSRF policy, and the connection is then pinned
// to that already-validated IP. A DNS-rebinding attacker therefore cannot swap
// the destination between the check and the connect. Every redirect target is
// re-validated with the same policy and the hop count is capped.
//
// enforce=false disables the policy entirely (used by the tool package's
// skipSSRFCheck test hook). The proxy is disabled because a proxy would resolve
// the hostname itself and defeat the pinned-IP guarantee.
func NewClient(timeout time.Duration, enforce bool, opts ...Option) *http.Client {
	cfg := clientConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}

	dialer := &net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: dialKeepAlive,
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           dialContext(dialer, enforce, &cfg),
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          maxIdleConns,
			IdleConnTimeout:       idleConnTimeout,
			TLSHandshakeTimeout:   tlsHandshakeTimeout,
			ExpectContinueTimeout: expectContinueTimeout,
		},
		CheckRedirect: redirectPolicy(maxSSRFRedirects, enforce, &cfg),
	}
}

// dialContext resolves the host once, validates every resolved IP, and dials
// only an address that already passed validation.
func dialContext(dialer *net.Dialer, enforce bool, cfg *clientConfig) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("split host port %q: %w", addr, err)
		}
		ips, err := resolveValidatedHost(ctx, host, enforce, cfg.loopbackAllowed(host, port))
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no usable address for %q", host)
		}
		return nil, fmt.Errorf("dial %q: %w", host, lastErr)
	}
}

// redirectPolicy returns a CheckRedirect hook that re-validates every redirect
// target with the shared SSRF policy and refuses extra hops. Validation runs
// under the same bounded context as CheckURL, so a redirect to a hostile
// resolver cannot stall the request indefinitely.
func redirectPolicy(maxHops int, enforce bool, cfg *clientConfig) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxHops {
			return fmt.Errorf("stopped after %d redirects", maxHops)
		}
		if !enforce {
			return nil
		}
		if err := checkURL(req.URL.String(), cfg.loopbackAllowed(req.URL.Hostname(), urlPort(req.URL))); err != nil {
			return fmt.Errorf("redirect blocked: %w", err)
		}
		return nil
	}
}

// urlPort returns the effective port of a URL, defaulting by scheme.
func urlPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}
