package agent

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHTTPMemoryClient covers the Hindsight-backed memory store end to end
// against a stub server: request shape, bearer token, response mapping and the
// limit applied to recalls.
func TestHTTPMemoryClient(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotAuth   string
		gotBody   map[string]any
	)
	var lastBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		lastBody, _ = io.ReadAll(r.Body)
		_ = json.Unmarshal(lastBody, &gotBody)

		switch r.URL.Path {
		case "/v1/default/banks/hermes/memories":
			w.WriteHeader(http.StatusOK)
		case "/v1/default/banks/hermes/memories/recall":
			io.WriteString(w, `{"results":[{"content":"v1","context":"k1"},{"content":"v2","context":"k2"}]}`)
		case "/v1/default/banks/hermes/memories/list":
			io.WriteString(w, `{"memory_units":[{"content":"lv","context":"lk"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// The client uses a package-level HTTP client; point it at the stub for the
	// duration of the test.
	prev := hindsightHTTP
	hindsightHTTP = srv.Client()
	t.Cleanup(func() { hindsightHTTP = prev })

	t.Setenv("HINDSIGHT_API_KEY", "secret")
	t.Setenv("OPENAI_API_KEY", "")

	c := NewHTTPMemoryClient(srv.URL)
	if err := c.Remember("k", "v"); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/default/banks/hermes/memories" {
		t.Fatalf("Remember used %s %s", gotMethod, gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q, want the Hindsight key", gotAuth)
	}
	items, ok := gotBody["items"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("Remember body = %s", lastBody)
	}
	first, _ := items[0].(map[string]any)
	if first["content"] != "v" || first["context"] != "k" {
		t.Fatalf("Remember body = %s, want the value and key", lastBody)
	}

	got, err := c.Recall("query", 1)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 1 || got[0].Key != "k1" || got[0].Value != "v1" {
		t.Fatalf("Recall(limit 1) = %+v", got)
	}
	got, err = c.Recall("query", 5)
	if err != nil || len(got) != 2 {
		t.Fatalf("Recall(limit 5) = %+v, %v", got, err)
	}
	if !strings.Contains(string(lastBody), `"budget":"mid"`) {
		t.Fatalf("Recall body = %s", lastBody)
	}

	list, err := c.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Key != "lk" || list[0].Value != "lv" {
		t.Fatalf("List = %+v", list)
	}

	// Forget is a documented no-op for this backend.
	if err := c.Forget("k"); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	// An empty base URL falls back to the built-in Hindsight endpoint, and the
	// token falls back to OPENAI_API_KEY when no Hindsight key is configured.
	if got := NewHTTPMemoryClient("").base(); got != hindsightBase {
		t.Fatalf("default base = %q, want %q", got, hindsightBase)
	}
	if got := NewHTTPMemoryClient(srv.URL).base(); got != srv.URL {
		t.Fatalf("configured base = %q, want %q", got, srv.URL)
	}
	t.Setenv("HINDSIGHT_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	if err := NewHTTPMemoryClient(srv.URL).Remember("k", "v"); err != nil {
		t.Fatalf("Remember with the OpenAI fallback: %v", err)
	}
	if gotAuth != "Bearer openai-key" {
		t.Fatalf("Authorization = %q, want the OpenAI fallback key", gotAuth)
	}
}

// TestHTTPMemoryClientErrors covers the failure paths: a non-2xx answer, a body
// that is not valid JSON, an unreachable server, a body that cannot be
// marshalled and an invalid request.
func TestHTTPMemoryClientErrors(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer failing.Close()

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "not json")
	}))
	defer broken.Close()

	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close()

	tests := []struct {
		name string
		call func(c *HTTPMemoryClient) error
		want string
	}{
		{"remember non-2xx", func(c *HTTPMemoryClient) error { return c.Remember("k", "v") }, "500"},
		{"recall non-2xx", func(c *HTTPMemoryClient) error { _, err := c.Recall("q", 1); return err }, "500"},
		{"list non-2xx", func(c *HTTPMemoryClient) error { _, err := c.List(); return err }, "500"},
		{"recall bad json", func(c *HTTPMemoryClient) error {
			_, err := NewHTTPMemoryClient(broken.URL).Recall("q", 1)
			return err
		}, "invalid"},
		{"list bad json", func(c *HTTPMemoryClient) error {
			_, err := NewHTTPMemoryClient(broken.URL).List()
			return err
		}, "invalid"},
	}
	for _, tc := range tests {
		if err := tc.call(NewHTTPMemoryClient(failing.URL)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error = %v, want it to mention %q", tc.name, err, tc.want)
		}
	}

	if err := NewHTTPMemoryClient(deadURL).Remember("k", "v"); err == nil {
		t.Error("a closed server must surface a transport error")
	}

	c := NewHTTPMemoryClient(failing.URL)
	if err := c.do(http.MethodPost, failing.URL, make(chan int), nil); err == nil || !strings.Contains(err.Error(), "marshal") {
		t.Errorf("unmarshalable body: error = %v, want a marshal error", err)
	}
	if err := c.do("BAD METHOD", failing.URL, nil, nil); err == nil || !strings.Contains(err.Error(), "request") {
		t.Errorf("invalid method: error = %v, want a request error", err)
	}
}
