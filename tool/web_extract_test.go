package tool

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func init() {
	skipSSRFCheck = true
}

func TestWebExtractBasicArticle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<!DOCTYPE html><html><body><article>
<h1>Article Title</h1>
<p>First paragraph with <strong>bold</strong> and <em>italic</em>.</p>
<p>Second paragraph with a <a href="https://example.com">link</a>.</p>
</article></body></html>`))
	}))
	defer server.Close()

	tool := WebExtract()
	result, err := tool.Execute(context.Background(), map[string]any{
		"urls": []any{server.URL},
	})
	if err != nil {
		t.Fatalf("extract error: %v", err)
	}
	if !strings.Contains(result, "**bold**") {
		t.Errorf("expected markdown bold, got:\n%s", result)
	}
	if !strings.Contains(result, "[link](https://example.com)") {
		t.Errorf("expected markdown link, got:\n%s", result)
	}
}

func TestWebExtractMultipleURLs(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Write([]byte(fmt.Sprintf(
			`<html><body><article><p>Page %d content here with enough text to pass truncation checks.</p></article></body></html>`,
			count)))
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
		t.Errorf("expected both pages, got: %q", result)
	}
}

func TestWebExtractMax5URLs(t *testing.T) {
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.Write([]byte(fmt.Sprintf(
			`<html><body><p>Content %d with enough text for the extraction to succeed.</p></body></html>`,
			count)))
	}))
	defer server.Close()

	urls := make([]any, 10)
	for i := 0; i < 10; i++ {
		urls[i] = server.URL
	}

	tool := WebExtract()
	result, err := tool.Execute(context.Background(), map[string]any{"urls": urls})
	if err != nil {
		t.Fatalf("extract error: %v", err)
	}
	pageCount := strings.Count(result, "=== ")
	if pageCount > 5 {
		t.Errorf("expected at most 5 pages, got %d", pageCount)
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
		t.Errorf("expected 'No content extracted', got: %q", result)
	}
}

func TestSSRFBlockPrivateIP(t *testing.T) {
	err := checkSSRF("http://192.168.1.1/admin")
	if err == nil {
		t.Fatal("expected error for private IP")
	}
}

func TestSSRFBlockMetadata(t *testing.T) {
	err := checkSSRF("http://169.254.169.254/latest/meta-data/")
	if err == nil {
		t.Fatal("expected error for metadata IP")
	}
}

func TestSSRFBlockLocalhost(t *testing.T) {
	err := checkSSRF("http://localhost:8545")
	if err == nil {
		t.Fatal("expected error for localhost")
	}
}

func TestSSRFAllowPublic(t *testing.T) {
	err := checkSSRF("https://golang.org")
	if err != nil {
		t.Fatalf("expected no error for public domain, got: %v", err)
	}
}
