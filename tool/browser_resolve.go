package tool

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// browserPinResolveTimeout bounds the DNS lookup used to build the pinning rule,
// so a slow or hostile resolver cannot stall the browser path.
const browserPinResolveTimeout = 5 * time.Second

// hostRuleFor renders a Chromium --host-resolver-rules entry that pins host to
// ip, or "" when pinning does not apply (the host is already an IP literal, or
// either argument is empty). It is a pure function so the rule format is
// unit-testable without a browser.
func hostRuleFor(host, ip string) string {
	host = strings.TrimSpace(host)
	ip = strings.TrimSpace(ip)
	if host == "" || ip == "" {
		return ""
	}
	if net.ParseIP(host) != nil {
		return "" // already an address: nothing to resolve, nothing to pin
	}
	if net.ParseIP(ip) == nil {
		return ""
	}
	// Chromium never resolves the host again, so a name that resolved publicly
	// during the check cannot be rebound to a private address for the browser.
	return fmt.Sprintf("MAP %s %s", host, ip)
}

// browserHostRule resolves the URL's host once with the shared SSRF policy and
// returns a --host-resolver-rules entry that pins Chromium to that exact IP.
//
// The returned rule closes DNS rebinding for the top-level host: the browser
// uses the address this process validated instead of resolving the name again.
// Redirect targets and subresource hosts are not pinned (their names only become
// known while the page loads); those are covered by the request interceptor.
//
// It returns "" when the rule does not apply or cannot be built safely: the
// skipSSRFCheck test hook, a non-http(s) scheme, an IP literal, or any
// resolution failure (in which case the request is refused elsewhere anyway).
func browserHostRule(rawURL string) string {
	if skipSSRFCheck {
		return ""
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	host := u.Hostname()
	if host == "" || net.ParseIP(host) != nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), browserPinResolveTimeout)
	defer cancel()

	ips, err := resolveValidatedHost(ctx, host, true)
	if err != nil || len(ips) == 0 {
		return ""
	}
	return hostRuleFor(host, ips[0].String())
}
