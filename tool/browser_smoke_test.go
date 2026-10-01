package tool

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yusiwen/tinycode/internal/browserproxy"
)

// smokePage is long enough for crawlViaExec's minimum-size check, and its text
// only appears after JavaScript runs, so a plain HTTP fetch cannot pass.
const smokePage = `<!doctype html>
<html><head><title>Smoke page</title></head>
<body>
<h1>Smoke</h1>
<p>This paragraph keeps the document comfortably above the minimum size the
extractor accepts, so a crawl that succeeds here really rendered the page
instead of returning an empty document that would be rejected.</p>
<div id="app">loading</div>
<script>document.getElementById('app').textContent = 'rendered-by-javascript';</script>
</body></html>`

// browserProbe records whether a page request arrived through the filtering
// proxy, which marks every request it forwards with a Via header.
type browserProbe struct {
	mu  sync.Mutex
	via []string
}

func (p *browserProbe) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		if v := r.Header.Get("Via"); v != "" {
			p.via = append(p.via, v)
		}
		p.mu.Unlock()
		io.WriteString(w, smokePage)
	}
}

func (p *browserProbe) sawProxy() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.via) > 0
}

// TestBrowserPathsRefuseToCrawlWithoutTheProxy pins the fail-closed policy of
// issue #8: when the filtering proxy cannot start, both browser paths return an
// error instead of crawling with the weaker pre-flight/interceptor protection.
// It needs no browser — the refusal happens before any launch.
func TestBrowserPathsRefuseToCrawlWithoutTheProxy(t *testing.T) {
	previousProxy := startBrowserProxy
	startBrowserProxy = func() *browserproxy.Proxy { return nil }
	t.Cleanup(func() { startBrowserProxy = previousProxy })

	// The pre-flight check is not what this test is about, and it would need DNS.
	previousSkip := skipSSRFCheck
	skipSSRFCheck = true
	t.Cleanup(func() { skipSSRFCheck = previousSkip })

	ctx := context.Background()
	if _, err := crawlViaExec(ctx, "/nonexistent/chrome", "http://example.com"); !errors.Is(err, errBrowserSandboxUnavailable) {
		t.Errorf("crawlViaExec = %v, want errBrowserSandboxUnavailable", err)
	}
	if _, err := crawlViaRod(ctx, "http://example.com"); !errors.Is(err, errBrowserSandboxUnavailable) {
		t.Errorf("crawlViaRod = %v, want errBrowserSandboxUnavailable", err)
	}
}

// sandboxProbePage is the document the refusal test serves. Both subresources are
// parser-blocking scripts, so the outcome is in the DOM by the time the document
// has loaded and `--dump-dom` (which does not wait for anything else) sees it: the
// allowed one proves the page really ran its subresources, the blocked one must
// have been refused by the filtering proxy.
func sandboxProbePage(allowedJS, blockedJS string) string {
	return `<!doctype html>
<html><head><title>Sandbox probe</title></head><body>
<h1>Sandbox probe</h1>
<p>This paragraph keeps the document comfortably above the minimum size the
extractor accepts, so a crawl that succeeds here really rendered the page.</p>
<div id="allowed">allowed=pending</div>
<div id="blocked">blocked=pending</div>
<script src="` + allowedJS + `"></script>
<script src="` + blockedJS + `" onerror="document.getElementById('blocked').textContent='blocked=refused'"></script>
</body></html>`
}

// startSandboxProbe starts the two services the refusal test needs: the page's
// own server (exempted from the loopback rule for exactly its authority) and a
// second, live loopback service that the proxy must refuse.
func startSandboxProbe(t *testing.T) (pageURL string, internalHits *atomic.Int64) {
	t.Helper()

	var hits atomic.Int64
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, "document.getElementById('blocked').textContent='blocked=loaded';\n")
	}))
	t.Cleanup(internal.Close)

	mux := http.NewServeMux()
	mux.HandleFunc("/allowed.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		io.WriteString(w, "document.getElementById('allowed').textContent='allowed=loaded';\n")
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, sandboxProbePage("http://"+r.Host+"/allowed.js", internal.URL+"/probe.js"))
	})
	page := httptest.NewServer(mux)
	t.Cleanup(page.Close)

	// The pre-flight check and the request interceptor are taken out of the way so
	// the proxy is the only component that can refuse the subresource; the policy
	// itself stays enforced *there*, and the exemption covers exactly the page's
	// authority — not the internal service on its other port.
	skipSSRFCheck = true
	browserSandboxConfig.forceEnforce = true
	browserSandboxConfig.allowAuthority = page.Listener.Addr().String()

	return page.URL + "/page", &hits
}

// assertBlockedSubresourceRefused checks the three properties the refusal test
// asserts: the page's own subresource loaded (so a failure is specific, not a
// blanket one), the subresource on the internal authority was refused, and the
// internal service was never reached.
func assertBlockedSubresourceRefused(t *testing.T, content string, err error, internalHits *atomic.Int64) {
	t.Helper()
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if !strings.Contains(content, "allowed=loaded") {
		t.Errorf("the page's own subresource did not load, so a refusal would prove nothing:\n%s", content)
	}
	if !strings.Contains(content, "blocked=refused") {
		t.Errorf("the subresource on the internal authority was not refused:\n%s", content)
	}
	if hits := internalHits.Load(); hits != 0 {
		t.Errorf("the internal service was reached %d time(s): the proxy let the subresource through", hits)
	}
}

// TestBrowserSmokeRefusesABlockedSubresource proves a *refusal*, not only that the
// proxy was used (issue #8). The page is served from loopback with a scoped
// exemption, its second subresource points at another live loopback service, and
// the proxy must refuse that one: if the policy regressed the script would load
// and the marker would read blocked=loaded.
//
// Both browser paths are covered because both are reachable without an
// interceptor — the `--dump-dom` path never had one.
func TestBrowserSmokeRefusesABlockedSubresource(t *testing.T) {
	if os.Getenv("BROWSER_TEST") == "" {
		t.Skip("skipping: set BROWSER_TEST=1 to run the real-browser smoke test")
	}
	previousSkip := skipSSRFCheck
	previousConfig := browserSandboxConfig
	t.Cleanup(func() {
		skipSSRFCheck = previousSkip
		browserSandboxConfig = previousConfig
	})

	t.Run("exec", func(t *testing.T) {
		// The path the production extractor takes for `--dump-dom`, headless-shell
		// preference included (issue #43).
		browserPath := findExecBrowser()
		if browserPath == "" {
			t.Skip("no Chromium/Chrome installed (system or Playwright cache)")
		}
		pageURL, internalHits := startSandboxProbe(t)
		content, err := crawlViaExec(context.Background(), browserPath, pageURL)
		assertBlockedSubresourceRefused(t, content, err, internalHits)
	})

	t.Run("rod", func(t *testing.T) {
		if findBrowser() == "" {
			t.Skip("no full Chromium/Chrome installed (the CDP path needs one)")
		}
		pageURL, internalHits := startSandboxProbe(t)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		content, err := crawlViaRod(ctx, pageURL)
		assertBlockedSubresourceRefused(t, content, err, internalHits)
	})
}

// TestBrowserSmokeThroughProxy exercises the two real browser paths against a
// local page. Both must render JavaScript and both must reach the page through
// the filtering proxy: without the `--proxy-bypass-list=<-loopback>` override
// Chromium would connect to a loopback target directly and the Via header would
// be missing, and without the proxy flags at all a subresource or redirect could
// leave without being validated.
func TestBrowserSmokeThroughProxy(t *testing.T) {
	if os.Getenv("BROWSER_TEST") == "" {
		t.Skip("skipping: set BROWSER_TEST=1 to run the real-browser smoke test")
	}
	previous := skipSSRFCheck
	skipSSRFCheck = true // the page is served from loopback
	t.Cleanup(func() { skipSSRFCheck = previous })

	t.Run("exec", func(t *testing.T) {
		browserPath := findExecBrowser()
		if browserPath == "" {
			t.Skip("no Chromium/Chrome installed (system or Playwright cache)")
		}
		t.Logf("using browser %s", browserPath)
		probe := &browserProbe{}
		srv := httptest.NewServer(probe.handler())
		defer srv.Close()

		content, err := crawlViaExec(context.Background(), browserPath, srv.URL)
		if err != nil {
			t.Fatalf("crawlViaExec: %v", err)
		}
		if !strings.Contains(content, "rendered-by-javascript") {
			t.Errorf("the JS-rendered text is missing from the output:\n%s", content)
		}
		if !probe.sawProxy() {
			t.Error("the page was not fetched through the filtering proxy (no Via header)")
		}
	})

	t.Run("rod", func(t *testing.T) {
		if findBrowser() == "" {
			t.Skip("no full Chromium/Chrome installed (the CDP path needs one)")
		}
		probe := &browserProbe{}
		srv := httptest.NewServer(probe.handler())
		defer srv.Close()

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		content, err := crawlViaRod(ctx, srv.URL)
		if err != nil {
			t.Fatalf("crawlViaRod: %v", err)
		}
		if !strings.Contains(content, "rendered-by-javascript") {
			t.Errorf("the JS-rendered text is missing from the output:\n%s", content)
		}
		if !probe.sawProxy() {
			t.Error("the page was not fetched through the filtering proxy (no Via header)")
		}
	})
}
