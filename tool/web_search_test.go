package tool

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSearchParseResults(t *testing.T) {
	// Mock DuckDuckGo Lite HTML response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!DOCTYPE html>
<html>
<body>
<table>
<tr>
<td class="result-snippet">The Go programming language official site</td>
</tr>
<tr>
<td><a class="result-link" href="https://golang.org">Go Programming Language</a></td>
</tr>
</table>
</body>
</html>`))
	}))
	defer server.Close()

	// Override DDG URL for testing
	savedURL := ddgLiteURL
	ddgLiteURL = server.URL
	defer func() { ddgLiteURL = savedURL }()

	results, err := searchDuckDuckGo(context.Background(), "golang", 5)
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 result")
	}
	if results[0].Title != "Go Programming Language" {
		t.Errorf("expected 'Go Programming Language', got %q", results[0].Title)
	}
	if results[0].URL != "https://golang.org" {
		t.Errorf("expected 'https://golang.org', got %q", results[0].URL)
	}
}

func TestWebSearchDeduplicate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!DOCTYPE html>
<html><body><table>
<tr><td class="result-snippet">First result</td></tr>
<tr><td><a class="result-link" href="https://example.com">Duplicate</a></td></tr>
<tr><td class="result-snippet">Also first</td></tr>
<tr><td><a class="result-link" href="https://example.com">Duplicate again</a></td></tr>
<tr><td class="result-snippet">Second unique</td></tr>
<tr><td><a class="result-link" href="https://example.org">Unique</a></td></tr>
</table></body></html>`))
	}))
	defer server.Close()

	savedURL := ddgLiteURL
	ddgLiteURL = server.URL
	defer func() { ddgLiteURL = savedURL }()

	results, err := searchDuckDuckGo(context.Background(), "test", 10)
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 unique results, got %d", len(results))
	}
}

func TestWebSearchEmptyQuery(t *testing.T) {
	tool := WebSearch()
	_, err := tool.Execute(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestWebSearchLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!DOCTYPE html>
<html><body><table>
<tr><td class="result-snippet">R1</td></tr><tr><td><a class="result-link" href="https://a.com">R1</a></td></tr>
<tr><td class="result-snippet">R2</td></tr><tr><td><a class="result-link" href="https://b.com">R2</a></td></tr>
<tr><td class="result-snippet">R3</td></tr><tr><td><a class="result-link" href="https://c.com">R3</a></td></tr>
<tr><td class="result-snippet">R4</td></tr><tr><td><a class="result-link" href="https://d.com">R4</a></td></tr>
<tr><td class="result-snippet">R5</td></tr><tr><td><a class="result-link" href="https://e.com">R5</a></td></tr>
</table></body></html>`))
	}))
	defer server.Close()

	savedURL := ddgLiteURL
	ddgLiteURL = server.URL
	defer func() { ddgLiteURL = savedURL }()

	tool := WebSearch()
	result, err := tool.Execute(context.Background(), map[string]any{
		"query": "test",
		"limit": 2,
	})
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if strings.Contains(result, "3.") {
		t.Error("expected max 2 results, got more")
	}
}

func TestWebSearchExecuteFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!DOCTYPE html>
<html><body><table>
<tr><td class="result-snippet">Desc text</td></tr>
<tr><td><a class="result-link" href="https://example.com/page">Title Here</a></td></tr>
</table></body></html>`))
	}))
	defer server.Close()

	savedURL := ddgLiteURL
	ddgLiteURL = server.URL
	defer func() { ddgLiteURL = savedURL }()

	tool := WebSearch()
	result, err := tool.Execute(context.Background(), map[string]any{"query": "test"})
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if !strings.Contains(result, "Title Here") {
		t.Errorf("expected 'Title Here' in output, got:\n%s", result)
	}
	if !strings.Contains(result, "https://example.com/page") {
		t.Errorf("expected URL in output")
	}
}
