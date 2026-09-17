package tool

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
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
	browserPath := findBrowser()
	if browserPath == "" {
		t.Skip("no Chromium/Chrome installed (system or Playwright cache)")
	}
	t.Logf("using browser %s", browserPath)

	previous := skipSSRFCheck
	skipSSRFCheck = true // the page is served from loopback
	t.Cleanup(func() { skipSSRFCheck = previous })

	t.Run("exec", func(t *testing.T) {
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
