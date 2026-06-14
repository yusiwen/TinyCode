package tool

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebExtractBasicArticle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<article>
<h1>Article Title</h1>
<p>This is the first paragraph with <strong>bold</strong> and <em>italic</em> text.</p>
<p>Second paragraph with a <a href="https://example.com">link</a>.</p>
</article>
</body>
</html>`))
	}))
	defer server.Close()

	tool := WebExtract()
	result, err := tool.Execute(context.Background(), map[string]any{
		"urls": []any{server.URL},
	})
	if err != nil {
		t.Fatalf("extract error: %v", err)
	}
	if !strings.Contains(result, "Article Title") {
		t.Errorf("expected 'Article Title', got:\n%s", result)
	}
	if !strings.Contains(result, "**bold**") {
		t.Errorf("expected '**bold**' (markdown), got:\n%s", result)
	}
	if !strings.Contains(result, "*italic*") {
		t.Errorf("expected '*italic*' (markdown), got:\n%s", result)
	}
	if !strings.Contains(result, "[link](https://example.com)") {
		t.Errorf("expected markdown link, got:\n%s", result)
	}
}

func TestWebExtractCodeBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!DOCTYPE html>
<html><body>
<article>
<h2>Code Example</h2>
<pre><code>package main
func main() {
    fmt.Println("hello")
}</code></pre>
</article>
</body></html>`))
	}))
	defer server.Close()

	tool := WebExtract()
	result, err := tool.Execute(context.Background(), map[string]any{
		"urls": []any{server.URL},
	})
	if err != nil {
		t.Fatalf("extract error: %v", err)
	}
	if !strings.Contains(result, "package main") {
		t.Errorf("expected code content, got:\n%s", result)
	}
}

func TestWebExtractList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!DOCTYPE html>
<html><body>
<article>
<ul>
<li>Item one</li>
<li>Item two</li>
<li>Item three</li>
</ul>
</article>
</body></html>`))
	}))
	defer server.Close()

	tool := WebExtract()
	result, err := tool.Execute(context.Background(), map[string]any{
		"urls": []any{server.URL},
	})
	if err != nil {
		t.Fatalf("extract error: %v", err)
	}
	if !strings.Contains(result, "Item one") {
		t.Errorf("expected 'Item one', got:\n%s", result)
	}
}

func TestWebExtractMultipleURLs(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Write([]byte(`<html><body><article><h1>Page ` + fmt.Sprintf("%d", count) + `</h1></article></body></html>`))
	}))
	defer server.Close()

	tool := WebExtract()
	result, err := tool.Execute(context.Background(), map[string]any{
		"urls": []any{server.URL, server.URL},
	})
	if err != nil {
		t.Fatalf("extract error: %v", err)
	}
	if !strings.Contains(result, "Page 1") || !strings.Contains(result, "Page 2") {
		t.Errorf("expected both pages, got:\n%s", result)
	}
}

func TestWebExtractMax5URLs(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Write([]byte(`<html><body><p>Page ` + fmt.Sprintf("%d", count) + `</p></body></html>`))
	}))
	defer server.Close()

	urls := make([]any, 10)
	for i := 0; i < 10; i++ {
		urls[i] = server.URL
	}

	tool := WebExtract()
	result, err := tool.Execute(context.Background(), map[string]any{
		"urls": urls,
	})
	if err != nil {
		t.Fatalf("extract error: %v", err)
	}
	// Should have at most 5 pages
	pageCount := strings.Count(result, "=== ")
	if pageCount > 5 {
		t.Errorf("expected at most 5 pages, got %d", pageCount)
	}
}

func TestWebExtractNoURLs(t *testing.T) {
	tool := WebExtract()
	_, err := tool.Execute(context.Background(), map[string]any{
		"urls": []any{},
	})
	if err == nil {
		t.Fatal("expected error for empty urls")
	}
}

func TestWebExtractNoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body><script>alert(1)</script></body></html>`))
	}))
	defer server.Close()

	tool := WebExtract()
	result, err := tool.Execute(context.Background(), map[string]any{
		"urls": []any{server.URL},
	})
	if err != nil {
		t.Fatalf("extract error: %v", err)
	}
	if !strings.Contains(result, "No content") {
		t.Errorf("expected 'No content extracted', got:\n%s", result)
	}
}
