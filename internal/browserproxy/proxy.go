// Package browserproxy runs a loopback HTTP proxy for the headless browser.
//
// Chromium resolves DNS itself, follows redirects itself and loads subresources
// itself, so a policy applied in this process before the browser starts can only
// see the top-level URL. Pointing the browser at this proxy makes every
// connection — the initial navigation, each redirect hop, XHR/fetch, iframes,
// images, WebSocket upgrades and the `--dump-dom` run — ask the proxy for a
// hostname. The proxy resolves each hostname exactly once, validates every
// resolved address against the shared SSRF policy and dials only a validated
// address, so a name that resolves to a private address, or is rebound between
// the check and the connection, never reaches it.
//
// HTTPS is tunnelled with CONNECT and stays end-to-end encrypted: the guarantee
// here is about which host is reached, not about inspecting the payload.
package browserproxy

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yusiwen/tinycode/internal/netsafe"
)

// proxyTimeout bounds one forwarded request. Chromium has its own page timeout;
// this only stops a stalled upstream from pinning a connection forever.
const proxyTimeout = 60 * time.Second

// hopByHopHeaders are connection-scoped and must not be forwarded; the client
// and the upstream each negotiate their own connection.
var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

// Proxy is a filtering forward proxy bound to a loopback port.
type Proxy struct {
	ln      net.Listener
	srv     *http.Server
	client  *http.Client
	enforce bool

	closeOnce sync.Once
	closeErr  error
}

// Start binds a proxy to an ephemeral loopback port and serves it in the
// background. enforce applies the SSRF policy to every target; it is false only
// in tests, which need to reach a local listener.
func Start(enforce bool) (*Proxy, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	p := &Proxy{
		ln:      ln,
		enforce: enforce,
		client:  netsafe.NewClient(proxyTimeout, enforce),
	}
	// The proxy must hand redirects back to the browser untouched: the browser
	// then asks the proxy for the next hop, which validates it like any other
	// request. Following redirects here would also silently drop the cookies a
	// hop sets, because this client has no cookie jar.
	p.client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	p.srv = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		// Serve returns ErrServerClosed on Close; nothing else can be reported
		// from here, so it is deliberately ignored.
		_ = p.srv.Serve(ln)
	}()
	return p, nil
}

// URL returns the address to hand to Chromium's --proxy-server flag.
func (p *Proxy) URL() string {
	return "http://" + p.ln.Addr().String()
}

// Close stops accepting connections and releases the listener.
func (p *Proxy) Close() error {
	p.closeOnce.Do(func() {
		p.closeErr = p.srv.Close()
	})
	return p.closeErr
}

// ServeHTTP dispatches between a tunnel (CONNECT) and a forwarded request.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.tunnel(w, r)
		return
	}
	p.forward(w, r)
}

// tunnel opens the validated upstream connection for a CONNECT request and then
// copies bytes in both directions.
func (p *Proxy) tunnel(w http.ResponseWriter, r *http.Request) {
	upstream, err := netsafe.DialValidatedContext(r.Context(), "tcp", r.Host, p.enforce)
	if err != nil {
		// A policy refusal is a decision; anything else is a broken upstream.
		status := http.StatusBadGateway
		if errors.Is(err, netsafe.ErrBlocked) {
			status = http.StatusForbidden
		}
		http.Error(w, fmt.Sprintf("proxy: %v", err), status)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "proxy: connection hijacking unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	defer client.Close()
	defer upstream.Close()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, client)
		// Half-close so the peer sees EOF instead of waiting for more bytes.
		if tcp, ok := upstream.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		if tcp, ok := client.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}()
	wg.Wait()
}

// forward relays a plain HTTP request. The response, including a redirect, is
// passed back unchanged so the browser decides what to do next.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	if r.URL == nil || !r.URL.IsAbs() || r.URL.Host == "" {
		http.Error(w, "proxy: absolute URL required", http.StatusBadRequest)
		return
	}
	if scheme := strings.ToLower(r.URL.Scheme); scheme != "http" {
		// https goes through CONNECT; anything else is not a web request.
		http.Error(w, "proxy: unsupported scheme "+r.URL.Scheme, http.StatusBadRequest)
		return
	}

	out := r.Clone(r.Context())
	out.RequestURI = "" // a client request must not carry the origin-form URI
	removeHopByHop(out.Header)
	if out.Header == nil {
		out.Header = http.Header{}
	}
	out.Header.Set("Via", "1.1 tinycode-browser-proxy")

	resp, err := p.client.Do(out)
	if err != nil {
		// A policy refusal is a decision, not a broken upstream.
		status := http.StatusBadGateway
		if errors.Is(err, netsafe.ErrBlocked) {
			status = http.StatusForbidden
		}
		http.Error(w, fmt.Sprintf("proxy: %v", err), status)
		return
	}
	defer resp.Body.Close()

	removeHopByHop(resp.Header)
	copyHeader(w.Header(), resp.Header)
	// An upstream that never declared a length is streamed, so let the client
	// see the same unknown length instead of a buffered one.
	if resp.ContentLength < 0 {
		w.Header().Del("Content-Length")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// removeHopByHop strips the connection-scoped headers.
func removeHopByHop(h http.Header) {
	if h == nil {
		return
	}
	for _, name := range hopByHopHeaders {
		h.Del(name)
	}
}

// copyHeader copies every value of src into dst.
func copyHeader(dst, src http.Header) {
	for name, values := range src {
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}
