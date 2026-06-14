package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSearchSearXNG(t *testing.T) {
	// Mock SearXNG JSON response
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"query":"test","results":[{"title":"Result A","url":"https://a.com","content":"Description A"},{"title":"Result B","url":"https://b.com","content":"Description B"}]}`))
	}))
	defer server.Close()

	// Configure SearXNG
	SetSearXNG(server.URL)

	results, err := searchSearXNG(context.Background(), "test", 10)
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	if results[0].Title != "Result A" {
		t.Errorf("expected 'Result A', got %q", results[0].Title)
	}
}

func TestWebSearchSearXNGViaExecute(t *testing.T) {
	// Simulate DDG failure + SearXNG success
	ddgServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer ddgServer.Close()

	searxngServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"results":[{"title":"Searched","url":"https://s.com","content":"Found it"}]}`))
	}))
	defer searxngServer.Close()

	// Override both backends
	savedDDG := ddgLiteURL
	ddgLiteURL = ddgServer.URL
	savedSearxng := searxngURL
	SetSearXNG(searxngServer.URL)
	defer func() {
		ddgLiteURL = savedDDG
		searxngURL = savedSearxng
	}()

	tool := WebSearch()
	result, err := tool.Execute(context.Background(), map[string]any{"query": "test"})
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if !strings.Contains(result, "Searched") {
		t.Errorf("expected 'Searched' from SearXNG, got:\n%s", result)
	}
}

func TestWebSearchSearXNGParse(t *testing.T) {
	// Test with various SearXNG JSON formats
	jsonStr := `{"results":[
		{"title":"Title 1","url":"https://1.com","content":"Content 1"},
		{"title":"Title 2","url":"https://2.com","content":"Content 2"}
	]}`

	results, err := parseSearXNGResults(jsonStr, 5)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestWebSearchSearXNGEdgeCases(t *testing.T) {
	// Empty results
	results, err := parseSearXNGResults(`{}`, 5)
	if err != nil {
		// Expect some kind of parse result
		if len(results) != 0 {
			t.Errorf("expected 0 results, got %d", len(results))
		}
	}

	// Results with special characters in content
	special := `{"results":[{"title":"Go 1.24","url":"https://go.dev","content":"\"range over func\" & \"weak references\""}]}`
	results, err = parseSearXNGResults(special, 5)
	if err != nil || len(results) == 0 {
		t.Errorf("expected parsed result with special chars, got %d results, err=%v", len(results), err)
	}
}

func TestWebSearchSearXNGNotConfigured(t *testing.T) {
	SetSearXNG("") // clear
	_, err := searchSearXNG(context.Background(), "test", 5)
	if err == nil {
		t.Fatal("expected error when SearXNG not configured")
	}
}

func TestWebSearchSearXNGLimit(t *testing.T) {
	// Generate 10 results in JSON
	var items []map[string]string
	for i := 0; i < 10; i++ {
		items = append(items, map[string]string{
			"title":   fmt.Sprintf("Result %d", i),
			"url":     fmt.Sprintf("https://%d.com", i),
			"content": fmt.Sprintf("Content %d", i),
		})
	}
	data, _ := json.Marshal(map[string]any{"results": items})

	results, err := parseSearXNGResults(string(data), 3)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if len(results) > 3 {
		t.Errorf("expected max 3 results, got %d", len(results))
	}
}

func TestWebSearchParseResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!DOCTYPE html><html><body><table>
<tr><td class="result-snippet">The Go programming language official site</td></tr>
<tr><td><a class="result-link" href="https://golang.org">Go Programming Language</a></td></tr>
</table></body></html>`))
	}))
	defer server.Close()

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
}

func TestWebSearchDeduplicate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!DOCTYPE html><html><body><table>
<tr><td class="result-snippet">First</td></tr>
<tr><td><a class="result-link" href="https://example.com">Duplicate</a></td></tr>
<tr><td class="result-snippet">Also first</td></tr>
<tr><td><a class="result-link" href="https://example.com">Duplicate again</a></td></tr>
<tr><td class="result-snippet">Second</td></tr>
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

func TestWebSearchLimitByDDG(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!DOCTYPE html><html><body><table>
<tr><td class="result-snippet">R1</td></tr><tr><td><a class="result-link" href="https://a.com">R1</a></td></tr>
<tr><td class="result-snippet">R2</td></tr><tr><td><a class="result-link" href="https://b.com">R2</a></td></tr>
<tr><td class="result-snippet">R3</td></tr><tr><td><a class="result-link" href="https://c.com">R3</a></td></tr>
</table></body></html>`))
	}))
	defer server.Close()

	savedURL := ddgLiteURL
	ddgLiteURL = server.URL
	defer func() { ddgLiteURL = savedURL }()

	tool := WebSearch()
	result, err := tool.Execute(context.Background(), map[string]any{"query": "test", "limit": 2})
	if err != nil {
		t.Fatalf("search error: %v", err)
	}
	if strings.Contains(result, "3.") {
		t.Error("expected max 2 results, got more")
	}
}

func TestWebSearchExecuteFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!DOCTYPE html><html><body><table>
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
	if !strings.Contains(result, "Title Here") || !strings.Contains(result, "https://example.com/page") {
		t.Errorf("expected title and URL in output, got:\n%s", result)
	}
}
